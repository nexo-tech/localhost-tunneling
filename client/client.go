package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"

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

	// Configure TLS
	tlsConfig := &tls.Config{}

	// Connect to WebSocket endpoint
	dialer := websocket.Dialer{
		TLSClientConfig: tlsConfig,
	}

	wsURL := fmt.Sprintf("wss://%s:%s/tunnel", serverIP, serverPort)
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to server")
	}
	defer conn.Close()
	log.Info("Successfully connected to server")

	// Send local port to server
	err = conn.WriteMessage(websocket.TextMessage, []byte(localPort))
	if err != nil {
		log.WithError(err).Fatal("Failed to send local port to server")
	}
	log.WithField("local_port", localPort).Info("Sent local port to server")

	for {
		// Read message from WebSocket
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.WithError(err).Error("Failed to read message")
			continue
		}

		// Connect to local service
		localConn, err := net.Dial("tcp", "localhost:"+localPort)
		if err != nil {
			log.WithError(err).Error("Failed to connect to local service")
			continue
		}
		defer localConn.Close()

		// Write received data to local service
		_, err = localConn.Write(message)
		if err != nil {
			log.WithError(err).Error("Failed to write to local service")
			continue
		}

		// Read response from local service
		buf := make([]byte, 1024)
		n, err := localConn.Read(buf)
		if err != nil {
			log.WithError(err).Error("Failed to read from local service")
			continue
		}

		// Send response back through WebSocket
		err = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
		if err != nil {
			log.WithError(err).Error("Failed to send response")
			continue
		}
	}
}
