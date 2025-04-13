package main

import (
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"localhost-tunneling/server/tunnel"
	"localhost-tunneling/server/ws"
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

	// Create WebSocket connection
	wsConn, err := ws.NewConnection(w, r)
	if err != nil {
		log.WithError(err).Error("Failed to create WebSocket connection")
		return
	}
	defer wsConn.Close()

	// Start ping ticker
	wsConn.StartPingTicker()

	// Get local port from client
	message, err := wsConn.ReadMessage()
	if err != nil {
		log.WithError(err).Error("Failed to read local port")
		return
	}
	if message == nil {
		log.Error("Expected text message for local port")
		return
	}
	localPort := string(message)
	log.WithField("local_port", localPort).Info("Received local port from client")

	// Create tunnel handler
	tunnelHandler := tunnel.NewHandler(wsConn, "")
	defer tunnelHandler.Close()

	// Assign port and subdomain
	port, err := tunnelHandler.AssignPort()
	if err != nil {
		log.WithError(err).Error("Failed to assign port")
		return
	}

	domain := os.Getenv("DOMAIN")
	if domain == "" {
		log.Error("DOMAIN environment variable not set")
		return
	}

	subdomain := fmt.Sprintf("%d.%s", port, domain)
	wsConn.SetDomain(subdomain)
	tunnelHandler.RegisterHost(subdomain, port)

	log.WithFields(logrus.Fields{
		"port":      port,
		"subdomain": subdomain,
	}).Info("Assigned port and subdomain")

	// Send public domain to client
	err = wsConn.WriteTextMessage(subdomain)
	if err != nil {
		log.WithError(err).Error("Failed to send public domain to client")
		return
	}

	// Start handling tunnel traffic
	tunnelHandler.Start()
}

func handleHTTPRequest(w http.ResponseWriter, r *http.Request) {
	log := log.WithFields(logrus.Fields{
		"method": r.Method,
		"path":   r.URL.Path,
		"host":   r.Host,
	})
	log.Info("Received HTTP request")

	// Create a temporary tunnel handler to handle the request
	tunnelHandler := tunnel.NewHandler(nil, "")
	tunnelHandler.HandleHTTPRequest(w, r)
}
