package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	yamux "github.com/hashicorp/yamux"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// WebSocket↔net.Conn adapter
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
			return 0, fmt.Errorf("expected binary, got %v", t)
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
	base := flag.String("base-domain-name", "", "tunnel.example.com (server only)")
	srvURL := flag.String("server-url", "", "wss://example.com/tunnel (client only)")
	flag.Parse()

	if *srvURL == "" {
		if *base == "" {
			log.Fatal("↳ server mode needs --base-domain-name")
		}
		runServer(*port, *base)
	} else {
		// client: positional arg for local port
		args := flag.Args()
		if len(args) > 0 {
			if p, err := fmt.Sscanf(args[0], "%d", port); err != nil || p != 1 {
				log.Fatal("↳ client needs <port> before flags")
			}
		}
		runClient(*port, *srvURL)
	}
}

func runServer(listenPort int, baseDomain string) {
	var (
		upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		sessions = make(map[string]*yamux.Session)
		mu       sync.RWMutex
	)
	// websocket endpoint for clients
	http.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("upgrade:", err)
			return
		}
		id := randString(8)
		ws.WriteJSON(map[string]string{"id": id, "base_domain": baseDomain})
		conn := newWSConn(ws)
		sess, err := yamux.Server(conn, nil)
		if err != nil {
			log.Println("yamux:", err)
			ws.Close()
			return
		}
		mu.Lock()
		sessions[id] = sess
		mu.Unlock()
		log.Printf("→ client registered: %s.%s\n", id, baseDomain)
		// cleanup on close
		go func() {
			<-sess.CloseChan()
			mu.Lock()
			delete(sessions, id)
			mu.Unlock()
			log.Printf("← client disconnected: %s\n", id)
		}()
	})

	// proxy HTTP based on subdomain
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		id := strings.TrimSuffix(host, "."+baseDomain)
		mu.RLock()
		sess := sessions[id]
		mu.RUnlock()
		if sess == nil {
			http.Error(w, "no such tunnel", 404)
			return
		}
		stream, err := sess.Open()
		if err != nil {
			http.Error(w, "tunnel error", 502)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "cannot hijack", 500)
			return
		}
		netConn, _, err := hj.Hijack()
		if err != nil {
			stream.Close()
			return
		}
		// send the raw HTTP request over the stream
		if err := r.Write(stream); err != nil {
			netConn.Close()
			stream.Close()
			return
		}
		// bidirectional copy
		go io.Copy(netConn, stream)
		go io.Copy(stream, netConn)
	})

	log.Printf("Server listening on :%d\n", listenPort)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", listenPort), nil))
}

func runClient(localPort int, serverURL string) {
	ws, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	var info struct {
		ID         string `json:"id"`
		BaseDomain string `json:"base_domain"`
	}
	if err := ws.ReadJSON(&info); err != nil {
		log.Fatal("handshake:", err)
	}
	fmt.Printf("▶ your tunnel is: %s.%s\n", info.ID, info.BaseDomain)

	conn := newWSConn(ws)
	sess, err := yamux.Client(conn, nil)
	if err != nil {
		log.Fatal("yamux:", err)
	}
	for {
		stream, err := sess.Accept()
		if err != nil {
			log.Println("session closed")
			return
		}
		go handleStream(stream, localPort)
	}
}

func handleStream(stream net.Conn, localPort int) {
	defer stream.Close()
	local, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return
	}
	defer local.Close()
	go io.Copy(local, stream)
	io.Copy(stream, local)
}
