package main

import (
	"os"

	"localhost-tunneling/client/tunnel"
	"localhost-tunneling/client/ws"

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

	// Create WebSocket connection
	wsConn := ws.NewConnection(serverIP, serverPort, localPort)
	defer wsConn.Close()

	// Connect to server
	if err := wsConn.Connect(); err != nil {
		log.WithError(err).Fatal("Failed to connect to server")
	}

	// Send local port and wait for domain
	if err := wsConn.SendLocalPort(); err != nil {
		log.WithError(err).Fatal("Failed to send local port")
	}

	if _, err := wsConn.WaitForDomain(); err != nil {
		log.WithError(err).Fatal("Failed to receive public domain")
	}

	// Start ping ticker
	wsConn.StartPingTicker()

	// Create and start tunnel handler
	tunnelHandler := tunnel.NewHandler(wsConn, localPort)
	defer tunnelHandler.Close()

	// Start handling tunnel traffic
	tunnelHandler.Start()
}
