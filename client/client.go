package main

import (
	"io"
	"net"
	"os"

	"github.com/hashicorp/yamux"
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

	serverAddress := net.JoinHostPort(serverIP, serverPort)
	log.WithField("server_address", serverAddress).Info("Connecting to server")

	conn, err := net.Dial("tcp", serverAddress)
	if err != nil {
		log.WithError(err).Fatal("Failed to connect to server")
	}
	defer conn.Close()
	log.Info("Successfully connected to server")

	// Send local port to server
	_, err = conn.Write([]byte(localPort))
	if err != nil {
		log.WithError(err).Fatal("Failed to send local port to server")
	}
	log.WithField("local_port", localPort).Info("Sent local port to server")

	// Yamux client session
	session, err := yamux.Client(conn, nil)
	if err != nil {
		log.WithError(err).Fatal("Failed to create yamux session")
	}
	log.Info("Created yamux session")

	for {
		stream, err := session.Accept()
		if err != nil {
			log.WithError(err).Fatal("Failed to accept stream")
		}
		log.WithField("stream_id", stream.RemoteAddr().String()).Info("Accepted new stream")
		go handleStream(stream, localPort)
	}
}

func handleStream(stream net.Conn, port string) {
	defer stream.Close()
	log := log.WithFields(logrus.Fields{
		"stream_id":  stream.RemoteAddr().String(),
		"local_port": port,
	})

	localConn, err := net.Dial("tcp", "localhost:"+port)
	if err != nil {
		log.WithError(err).Error("Failed to connect to local service")
		return
	}
	defer localConn.Close()
	log.Info("Connected to local service")

	go func() {
		bytes, err := io.Copy(stream, localConn)
		log.WithFields(logrus.Fields{
			"bytes": bytes,
			"error": err,
		}).Info("Finished copying from local to remote")
	}()

	bytes, err := io.Copy(localConn, stream)
	log.WithFields(logrus.Fields{
		"bytes": bytes,
		"error": err,
	}).Info("Finished copying from remote to local")
}
