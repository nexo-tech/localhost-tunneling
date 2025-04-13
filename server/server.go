package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

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
	// Read configuration from environment
	domain := os.Getenv("DOMAIN")
	tunnelDomain := os.Getenv("TUNNEL_SERVER_DOMAIN_NAME")
	proxyPort := os.Getenv("CADDY_PROXY_PORT")
	if proxyPort == "" {
		proxyPort = "3000"
	}

	if domain == "" || tunnelDomain == "" {
		log.Fatal("Missing required environment variables: DOMAIN and TUNNEL_SERVER_DOMAIN_NAME")
	}

	log.WithFields(logrus.Fields{
		"domain":        domain,
		"tunnel_domain": tunnelDomain,
		"proxy_port":    proxyPort,
	}).Info("Starting tunnel server")

	// Start HTTP server with WebSocket support
	http.HandleFunc("/tunnel", handleTunnel)
	http.HandleFunc("/", handleHTTPRequest)

	addr := ":" + proxyPort
	log.WithField("address", addr).Info("Listening for connections")

	server := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.WithFields(logrus.Fields{
				"method": r.Method,
				"path":   r.URL.Path,
				"host":   r.Host,
			}).Info("Incoming request")
			http.DefaultServeMux.ServeHTTP(w, r)
		}),
	}

	log.Fatal(server.ListenAndServe())
}

func handleTunnel(w http.ResponseWriter, r *http.Request) {
	log := log.WithFields(logrus.Fields{
		"method": r.Method,
		"path":   r.URL.Path,
		"host":   r.Host,
	})
	log.Info("Received tunnel connection request")

	// Upgrade to WebSocket with ping/pong support
	upgrader := websocket.Upgrader{
		CheckOrigin:       func(r *http.Request) bool { return true },
		HandshakeTimeout:  10 * time.Second,
		EnableCompression: true,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("Failed to upgrade to WebSocket")
		return
	}
	defer conn.Close()

	// Set up ping/pong handler
	conn.SetPingHandler(func(appData string) error {
		log.Debug("Received ping, sending pong")
		return conn.WriteMessage(websocket.PongMessage, []byte(appData))
	})

	// Set up pong handler
	conn.SetPongHandler(func(appData string) error {
		log.Debug("Received pong")
		return nil
	})

	// Start ping ticker
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Start connection monitoring
	connCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start goroutine to send pings
	go func() {
		for {
			select {
			case <-pingTicker.C:
				if err := conn.WriteMessage(websocket.PingMessage, []byte("ping")); err != nil {
					log.WithError(err).Error("Failed to send ping")
					cancel()
					return
				}
			case <-connCtx.Done():
				return
			}
		}
	}()

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

	subdomain := fmt.Sprintf("%d.%s", serverPort, os.Getenv("DOMAIN"))
	hostMap[subdomain] = serverPort

	log.WithFields(logrus.Fields{
		"server_port": serverPort,
		"subdomain":   subdomain,
	}).Info("Assigned new port and subdomain")

	// Send public domain to client
	err = conn.WriteMessage(websocket.TextMessage, []byte(subdomain))
	if err != nil {
		log.WithError(err).Error("Failed to send public domain to client")
		return
	}
	log.Info("Sent public domain to client")

	// Configure Caddy
	err = configureCaddy(subdomain, serverPort)
	if err != nil {
		log.WithError(err).Error("Failed to configure Caddy")
		return
	}
	log.Info("Successfully configured Caddy")

	// Start port listener
	go startPortListener(serverPort, conn)

	// Wait for context cancellation or connection close
	<-connCtx.Done()
	log.Info("Connection closed")
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

	// Create a context for managing the listener
	listenerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a goroutine to handle incoming connections
	go func() {
		for {
			select {
			case <-listenerCtx.Done():
				return
			default:
				conn, err := ln.Accept()
				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						log.WithError(err).Error("Failed to accept connection")
					}
					continue
				}

				log.WithField("remote_addr", conn.RemoteAddr().String()).Info("New connection accepted")

				// Create a context for this connection
				connCtx, connCancel := context.WithCancel(listenerCtx)

				// Start goroutines to handle the connection
				go func(c net.Conn) {
					defer func() {
						c.Close()
						connCancel()
					}()

					// Create a message buffer for WebSocket messages
					messageBuffer := make([]byte, 0, 4096)

					// Read from WebSocket and write to connection
					go func() {
						for {
							select {
							case <-connCtx.Done():
								return
							default:
								messageType, message, err := wsConn.ReadMessage()
								if err != nil {
									if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
										log.WithError(err).Error("Failed to read from WebSocket")
									}
									return
								}

								// Only process binary messages
								if messageType != websocket.BinaryMessage {
									continue
								}

								// Write the complete message to the connection
								_, err = c.Write(message)
								if err != nil {
									if !errors.Is(err, net.ErrClosed) {
										log.WithError(err).Error("Failed to write to connection")
									}
									return
								}
							}
						}
					}()

					// Read from connection and write to WebSocket
					buf := make([]byte, 4096)
					for {
						select {
						case <-connCtx.Done():
							return
						default:
							n, err := c.Read(buf)
							if err != nil {
								if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
									log.WithError(err).Error("Failed to read from connection")
								}
								return
							}

							// Append to message buffer
							messageBuffer = append(messageBuffer, buf[:n]...)

							// If we have a complete message, send it
							if len(messageBuffer) > 0 {
								err = wsConn.WriteMessage(websocket.BinaryMessage, messageBuffer)
								if err != nil {
									if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
										log.WithError(err).Error("Failed to write to WebSocket")
									}
									return
								}
								// Clear the buffer
								messageBuffer = messageBuffer[:0]
							}
						}
					}
				}(conn)
			}
		}
	}()

	// Wait for WebSocket connection to close
	<-listenerCtx.Done()
	log.Info("Port listener shutting down")
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

	// Create a new request to the local service
	localURL := fmt.Sprintf("http://localhost:%d%s", port, r.URL.Path)
	req, err := http.NewRequest(r.Method, localURL, r.Body)
	if err != nil {
		log.WithError(err).Error("Failed to create request")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Copy headers from original request
	for name, values := range r.Header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	// Create HTTP client
	client := &http.Client{}

	// Forward the request
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Error("Failed to forward request")
		http.Error(w, "Failed to connect to local service", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}

	// Set status code
	w.WriteHeader(resp.StatusCode)

	// Copy response body
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.WithError(err).Error("Failed to copy response body")
	}
}
