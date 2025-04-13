package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/sirupsen/logrus"
)

var (
	portMutex   sync.Mutex
	nextPort    = 10000
	activePorts = make(map[int]bool)
	hostMap     = make(map[string]int)
	log         = logrus.New()
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

	// Start control server
	go startControlServer(4000)
	log.WithField("port", 4000).Info("Started control server")

	// Start HTTP server for Caddy
	http.HandleFunc("/", handleHTTPRequest)
	log.WithField("port", 3000).Info("Starting HTTP server")
	log.Fatal(http.ListenAndServe(":3000", nil))
}

func startControlServer(port int) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.WithError(err).Fatal("Failed to start control server")
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.WithError(err).Error("Failed to accept connection")
			continue
		}
		log.WithField("remote_addr", conn.RemoteAddr().String()).Info("New client connected")
		go handleClient(conn)
	}
}

func handleClient(conn net.Conn) {
	defer conn.Close()
	log := log.WithField("remote_addr", conn.RemoteAddr().String())

	// Yamux session for multiplexing
	session, err := yamux.Server(conn, nil)
	if err != nil {
		log.WithError(err).Error("Failed to create yamux session")
		return
	}
	log.Info("Created yamux session")

	// Get local port from client
	buf := make([]byte, 10)
	_, err = conn.Read(buf)
	if err != nil {
		log.WithError(err).Error("Failed to read local port from client")
		return
	}

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
	go startPortListener(serverPort, session)

	// Keep connection open
	<-context.Background().Done()
}

func startPortListener(port int, session *yamux.Session) {
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
			stream, err := session.Open()
			if err != nil {
				log.WithError(err).Error("Failed to open stream")
				return
			}
			defer stream.Close()

			go func() {
				bytes, err := io.Copy(c, stream)
				log.WithFields(logrus.Fields{
					"bytes": bytes,
					"error": err,
				}).Info("Finished copying from stream to connection")
			}()

			bytes, err := io.Copy(stream, c)
			log.WithFields(logrus.Fields{
				"bytes": bytes,
				"error": err,
			}).Info("Finished copying from connection to stream")
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
