package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	yamux "github.com/hashicorp/yamux"
	"github.com/sirupsen/logrus"
)

func init() {
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: time.RFC3339,
	})
	logrus.SetLevel(logrus.DebugLevel)
}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// wsConn adapts a websocket.Conn to net.Conn for yamux
type wsConn struct {
	ws      *websocket.Conn
	readBuf bytes.Buffer
}

func newWSConn(ws *websocket.Conn) net.Conn {
	return &wsConn{ws: ws}
}

func (c *wsConn) Read(p []byte) (int, error) {
	if c.readBuf.Len() == 0 {
		t, r, err := c.ws.NextReader()
		if err != nil {
			return 0, err
		}
		if t != websocket.BinaryMessage {
			return 0, fmt.Errorf("expected binary message, got %v", t)
		}
		_, err = io.Copy(&c.readBuf, r)
		if err != nil {
			return 0, err
		}
	}
	return c.readBuf.Read(p)
}

func (c *wsConn) Write(p []byte) (int, error) {
	w, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, err := w.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.Close()
}

func (c *wsConn) Close() error         { return c.ws.Close() }
func (c *wsConn) LocalAddr() net.Addr  { return dummyAddr(c.ws.LocalAddr().String()) }
func (c *wsConn) RemoteAddr() net.Addr { return dummyAddr(c.ws.RemoteAddr().String()) }
func (c *wsConn) SetDeadline(t time.Time) error {
	c.ws.SetReadDeadline(t)
	return c.ws.SetWriteDeadline(t)
}
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

type dummyAddr string

func (d dummyAddr) Network() string { return "ws" }
func (d dummyAddr) String() string  { return string(d) }

func main() {
	port := flag.Int("port", 8080, "server listen port or client local port")
	base := flag.String("base-domain-name", "", "tunnel.example.com (server)")
	srvURL := flag.String("server-url", "", "wss://example.com/tunnel (client)")
	flag.Parse()

	if *srvURL == "" {
		logrus.WithFields(logrus.Fields{"port": *port, "base_domain": *base}).Info("Starting server mode")
		runServer(*port, *base)
	} else {
		logrus.WithFields(logrus.Fields{"port": *port, "server_url": *srvURL}).Info("Starting client mode")
		runClient(*port, *srvURL)
	}
}

func runServer(listenPort int, baseDomain string) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	sessions := make(map[string]*yamux.Session)
	var mu sync.RWMutex

	// WebSocket endpoint for client tunnels
	http.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logrus.WithError(err).Error("WebSocket upgrade failed")
			return
		}
		id := randString(8)
		handshake := map[string]string{"id": id, "base_domain": baseDomain}
		ws.WriteJSON(handshake)
		conn := newWSConn(ws)
		sess, err := yamux.Server(conn, nil)
		if err != nil {
			logrus.WithError(err).Error("Failed to create yamux server session")
			ws.Close()
			return
		}
		mu.Lock()
		sessions[id] = sess
		mu.Unlock()
		logrus.WithFields(logrus.Fields{"id": id, "base_domain": baseDomain}).Info("Client registered")

		go func() {
			<-sess.CloseChan()
			mu.Lock()
			delete(sessions, id)
			mu.Unlock()
			logrus.WithField("id", id).Info("Client disconnected")
		}()
	})

	// Proxy HTTP based on subdomain
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		// strip port if present
		hostName := host
		if strings.Contains(hostName, ":") {
			hostName, _, _ = net.SplitHostPort(hostName)
		}
		id := strings.TrimSuffix(hostName, "."+baseDomain)
		logrus.WithFields(logrus.Fields{"host": hostName, "tunnel_id": id, "remote_addr": r.RemoteAddr}).Info("Incoming HTTP request")

		mu.RLock()
		sess := sessions[id]
		mu.RUnlock()
		if sess == nil {
			logrus.WithField("id", id).Warn("No such tunnel")
			http.Error(w, "no such tunnel", http.StatusNotFound)
			return
		}

		stream, err := sess.Open()
		if err != nil {
			logrus.WithError(err).Error("Failed to open yamux stream")
			http.Error(w, "tunnel error", http.StatusBadGateway)
			return
		}
		logrus.Debugf("Opened yamux stream for tunnel %s", id)

		hj, ok := w.(http.Hijacker)
		if !ok {
			logrus.Error("ResponseWriter does not support hijack")
			http.Error(w, "cannot hijack", http.StatusInternalServerError)
			return
		}
		netConn, _, err := hj.Hijack()
		if err != nil {
			stream.Close()
			logrus.WithError(err).Error("HTTP hijack failed")
			return
		}

		// Forward raw HTTP request over stream
		if err := r.Write(stream); err != nil {
			netConn.Close()
			stream.Close()
			logrus.WithError(err).Error("Failed to forward request to stream")
			return
		}

		// Relay response
		go func() {
			io.Copy(netConn, stream)
			netConn.Close()
			stream.Close()
		}()
		go func() {
			io.Copy(stream, netConn)
			stream.Close()
			netConn.Close()
		}()
	})

	addr := fmt.Sprintf(":%d", listenPort)
	logrus.WithField("listen_addr", addr).Info("Server listening")
	logrus.Fatal(http.ListenAndServe(addr, nil))
}

func runClient(localPort int, serverURL string) {
	ws, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to dial server")
	}
	logrus.WithField("server_url", serverURL).Info("WebSocket connection established")

	var info struct {
		ID         string `json:"id"`
		BaseDomain string `json:"base_domain"`
	}
	if err := ws.ReadJSON(&info); err != nil {
		logrus.WithError(err).Fatal("Handshake failed")
	}
	logrus.WithFields(logrus.Fields{"id": info.ID, "base_domain": info.BaseDomain}).Info("Handshake info received")
	fmt.Printf("▶ Tunnel ready at %s.%s\n", info.ID, info.BaseDomain)

	conn := newWSConn(ws)
	sess, err := yamux.Client(conn, nil)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to create yamux client session")
	}
	logrus.Info("Yamux session established")

	for {
		stream, err := sess.Accept()
		if err != nil {
			logrus.WithError(err).Info("Yamux session closed")
			return
		}
		logrus.Debug("New yamux stream accepted")
		go handleStream(stream, localPort)
	}
}

func handleStream(stream net.Conn, localPort int) {
	defer stream.Close()
	logrus.WithField("local_port", localPort).Debug("Handling new stream")

	local, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", localPort))
	if err != nil {
		logrus.WithError(err).Error("Failed to connect to local service")
		return
	}
	defer local.Close()

	// Copy data
	go io.Copy(local, stream)
	io.Copy(stream, local)
}
