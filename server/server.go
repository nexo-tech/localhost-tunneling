package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

var (
	portMutex   sync.Mutex
	nextPort    = 10000
	activePorts = make(map[int]bool)
	hostMap     = make(map[string]int)
	log         = logrus.New()
	upgrader    = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func init() {
	// Configure logrus for nice terminal output
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceColors:     true,
	})
	log.SetOutput(os.Stdout)
	log.SetLevel(logrus.DebugLevel)
}

func main() {
	log.Info("Starting tunnel server")

	// Start HTTP server with WebSocket support
	http.HandleFunc("/tunnel", handleTunnel)
	http.HandleFunc("/", handleHTTPRequest)
	log.WithField("port", 3000).Info("Starting HTTP server")
	log.Fatal(http.ListenAndServe(":3000", nil))
}

func handleTunnel(w http.ResponseWriter, r *http.Request) {
	log := log.WithFields(logrus.Fields{
		"method": r.Method,
		"path":   r.URL.Path,
		"host":   r.Host,
	})
	log.Info("Received tunnel connection request")

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("Failed to upgrade to WebSocket")
		return
	}
	defer conn.Close()
	log.Info("Successfully upgraded to WebSocket")

	// Get local port from client
	_, message, err := conn.ReadMessage()
	if err != nil {
		log.WithError(err).Error("Failed to read local port")
		return
	}
	localPort := string(message)
	log.WithField("local_port", localPort).Info("Received local port from client")

	// Assign port and subdomain
	portMutex.Lock()
	serverPort := nextPort
	for activePorts[serverPort] {
		serverPort++
	}
	nextPort = serverPort + 1
	activePorts[serverPort] = true
	portMutex.Unlock()

	subdomain := fmt.Sprintf("%d.tunnel.yourdomain.com", serverPort)
	hostMap[subdomain] = serverPort

	log.WithFields(logrus.Fields{
		"server_port": serverPort,
		"subdomain":   subdomain,
	}).Info("Assigned new port and subdomain")

	// Configure Caddy
	err = configureCaddy(subdomain, serverPort)
	if err != nil {
		log.WithError(err).Error("Failed to configure Caddy")
		return
	}
	log.Info("Successfully configured Caddy")

	// Start port listener
	go startPortListener(serverPort, conn)

	// Keep connection open
	<-context.Background().Done()
}

func startPortListener(port int, wsConn *websocket.Conn) {
	log := log.WithField("port", port)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.WithError(err).Error("Failed to start port listener")
		return
	}
	defer ln.Close()
	log.Info("Started port listener")

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.WithError(err).Error("Failed to accept connection")
			continue
		}
		log.WithField("remote_addr", conn.RemoteAddr().String()).Info("New connection accepted")

		go func(c net.Conn) {
			defer c.Close()

			// Read from WebSocket and write to connection
			go func() {
				for {
					_, message, err := wsConn.ReadMessage()
					if err != nil {
						log.WithError(err).Error("Failed to read from WebSocket")
						return
					}
					_, err = c.Write(message)
					if err != nil {
						log.WithError(err).Error("Failed to write to connection")
						return
					}
				}
			}()

			// Read from connection and write to WebSocket
			buf := make([]byte, 1024)
			for {
				n, err := c.Read(buf)
				if err != nil {
					log.WithError(err).Error("Failed to read from connection")
					return
				}
				err = wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
				if err != nil {
					log.WithError(err).Error("Failed to write to WebSocket")
					return
				}
			}
		}(conn)
	}
}

func configureCaddy(subdomain string, port int) error {
	log := log.WithFields(logrus.Fields{
		"subdomain": subdomain,
		"port":      port,
	})

	config := map[string]interface{}{
		"@id": subdomain,
		"match": []map[string]interface{}{{
			"host": []string{subdomain},
		}},
		"handle": []map[string]interface{}{{
			"handler": "reverse_proxy",
			"upstreams": []map[string]interface{}{{
				"dial": fmt.Sprintf("localhost:%d", port),
			}},
		}},
	}

	jsonData, _ := json.Marshal(config)
	req, _ := http.NewRequest(
		"POST",
		"http://localhost:2019/config/apps/http/servers/srv0/routes",
		bytes.NewBuffer(jsonData),
	)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Error("Failed to send Caddy configuration")
		return err
	}
	defer resp.Body.Close()

	log.Info("Successfully configured Caddy")
	return nil
}

func handleHTTPRequest(w http.ResponseWriter, r *http.Request) {
	log := log.WithFields(logrus.Fields{
		"host": r.Host,
		"path": r.URL.Path,
	})

	host := r.Host
	port, exists := hostMap[host]
	if !exists {
		log.Error("Tunnel not found")
		http.Error(w, "Tunnel not found", http.StatusNotFound)
		return
	}

	log.WithField("port", port).Info("Forwarding request")
	http.Redirect(w, r, fmt.Sprintf("http://localhost:%d%s", port, r.URL.Path), http.StatusFound)
}
