package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

var log = logrus.New()

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

func connectToServer(serverIP, serverPort, localPort string) (*websocket.Conn, string, error) {
	// Configure TLS
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // For debugging only
	}

	// Create custom dialer with timeout
	dialer := websocket.Dialer{
		TLSClientConfig:   tlsConfig,
		HandshakeTimeout:  10 * time.Second,
		EnableCompression: true,
	}

	// Add custom headers
	headers := http.Header{}
	headers.Add("User-Agent", "tunnel-client/1.0")

	// Construct URL without port if it's 443
	var wsURL string
	if serverPort == "443" {
		wsURL = fmt.Sprintf("wss://%s/tunnel", serverIP)
	} else {
		wsURL = fmt.Sprintf("wss://%s:%s/tunnel", serverIP, serverPort)
	}

	log.WithFields(logrus.Fields{
		"url":     wsURL,
		"headers": headers,
	}).Debug("Attempting WebSocket connection")

	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			log.WithFields(logrus.Fields{
				"status":     resp.Status,
				"statusCode": resp.StatusCode,
				"headers":    resp.Header,
			}).Error("Response details")
		}
		return nil, "", err
	}

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

	// Send local port to server
	err = conn.WriteMessage(websocket.TextMessage, []byte(localPort))
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	log.WithField("local_port", localPort).Info("Sent local port to server")

	// Wait for public domain assignment
	messageType, message, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	if messageType != websocket.TextMessage {
		conn.Close()
		return nil, "", fmt.Errorf("expected text message for domain assignment, got %d", messageType)
	}
	publicDomain := string(message)
	log.WithField("public_domain", publicDomain).Info("Received public domain assignment")

	return conn, publicDomain, nil
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: tunnel-client <local-port>")
	}
	localPort := os.Args[1]
	log.WithField("local_port", localPort).Info("Starting tunnel client")

	serverIP := os.Getenv("TUNNEL_SERVER_IP")
	serverPort := os.Getenv("TUNNEL_SERVER_PORT")
	if serverPort == "" {
		serverPort = "443"
	}

	if serverPort == "" || serverIP == "" {
		log.WithFields(logrus.Fields{
			"server_ip":   serverIP,
			"server_port": serverPort,
		}).Fatal("Missing required environment variables")
	}

	var conn *websocket.Conn
	// var publicDomain string
	var err error

	// Retry connection with exponential backoff
	for i := 0; i < 5; i++ {
		conn, _, err = connectToServer(serverIP, serverPort, localPort)
		if err == nil {
			break
		}
		waitTime := time.Duration(1<<uint(i)) * time.Second
		log.WithFields(logrus.Fields{
			"attempt": i + 1,
			"wait":    waitTime,
			"error":   err,
		}).Error("Failed to connect, retrying...")
		time.Sleep(waitTime)
	}
	if err != nil {
		log.WithError(err).Fatal("Failed to connect after retries")
	}
	defer conn.Close()

	// Set up a channel to handle incoming messages
	messageChan := make(chan []byte)
	errorChan := make(chan error)

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

	// Start a goroutine to read messages
	go func() {
		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				errorChan <- err
				return
			}
			if messageType == websocket.BinaryMessage {
				messageChan <- message
			}
		}
	}()

	// Main loop to handle messages
	for {
		select {
		case message := <-messageChan:
			log.WithFields(logrus.Fields{
				"length": len(message),
			}).Debug("Received message from server")

			// Connect to local service
			localConn, err := net.Dial("tcp", "localhost:"+localPort)
			if err != nil {
				log.WithError(err).Error("Failed to connect to local service")
				continue
			}
			defer localConn.Close()

			log.Debug("Connected to local service")

			// Write received data to local service
			_, err = localConn.Write(message)
			if err != nil {
				log.WithError(err).Error("Failed to write to local service")
				continue
			}

			log.Debug("Forwarded request to local service")

			// Read response from local service
			buf := make([]byte, 1024)
			n, err := localConn.Read(buf)
			if err != nil {
				log.WithError(err).Error("Failed to read from local service")
				continue
			}

			log.WithFields(logrus.Fields{
				"bytes": n,
			}).Debug("Received response from local service")

			// Send response back through WebSocket
			err = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			if err != nil {
				log.WithError(err).Error("Failed to send response")
				continue
			}

			log.Debug("Forwarded response to server")

		case err := <-errorChan:
			log.WithError(err).Error("WebSocket read error")
			// Attempt to reconnect
			for i := range 5 {
				conn, _, err = connectToServer(serverIP, serverPort, localPort)
				if err == nil {
					log.Info("Successfully reconnected to server")
					// Restart the message reading goroutine
					go func() {
						for {
							messageType, message, err := conn.ReadMessage()
							if err != nil {
								errorChan <- err
								return
							}
							if messageType == websocket.BinaryMessage {
								messageChan <- message
							}
						}
					}()
					break
				}
				waitTime := time.Duration(1<<uint(i)) * time.Second
				log.WithFields(logrus.Fields{
					"attempt": i + 1,
					"wait":    waitTime,
					"error":   err,
				}).Error("Failed to reconnect, retrying...")
				time.Sleep(waitTime)
			}
			if err != nil {
				log.WithError(err).Fatal("Failed to reconnect after retries")
			}

		case <-connCtx.Done():
			log.Info("Connection closed")
			return
		}
	}
}
