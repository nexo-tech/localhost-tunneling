package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"localhost-tunneling/server/ws"

	"github.com/sirupsen/logrus"
)

var (
	log         = logrus.New()
	portMutex   sync.Mutex
	nextPort    = 10000
	activePorts = make(map[int]bool)
	hostMap     = make(map[string]int)
)

type Handler struct {
	wsConn *ws.Connection
	domain string
	ctx    context.Context
	cancel context.CancelFunc
}

func NewHandler(wsConn *ws.Connection, domain string) *Handler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Handler{
		wsConn: wsConn,
		domain: domain,
		ctx:    ctx,
		cancel: cancel,
	}
}

func (h *Handler) Start() {
	// Set up channels for communication
	messageChan := make(chan []byte)
	errorChan := make(chan error)

	// Start message reading goroutine
	go h.readMessages(messageChan, errorChan)

	// Main loop to handle messages
	for {
		select {
		case message := <-messageChan:
			h.handleMessage(message)

		case err := <-errorChan:
			log.WithError(err).Error("WebSocket read error")
			h.Close()
			return

		case <-h.ctx.Done():
			log.Info("Tunnel handler shutting down")
			return
		}
	}
}

func (h *Handler) readMessages(messageChan chan []byte, errorChan chan error) {
	for {
		select {
		case <-h.ctx.Done():
			return
		default:
			message, err := h.wsConn.ReadMessage()
			if err != nil {
				errorChan <- err
				return
			}
			if message != nil {
				messageChan <- message
			}
		}
	}
}

func (h *Handler) handleMessage(message []byte) {
	log.WithFields(logrus.Fields{
		"length": len(message),
	}).Debug("Received message from client")

	port := h.wsConn.GetPort()
	if port == 0 {
		log.Error("No port assigned for this connection")
		return
	}

	// Connect to local service
	localConn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		log.WithError(err).Error("Failed to connect to local service")
		return
	}
	defer localConn.Close()

	log.Debug("Connected to local service")

	// Write received data to local service
	_, err = localConn.Write(message)
	if err != nil {
		log.WithError(err).Error("Failed to write to local service")
		return
	}

	log.Debug("Forwarded request to local service")

	// Read response from local service
	buf := make([]byte, 4096)
	n, err := localConn.Read(buf)
	if err != nil {
		log.WithError(err).Error("Failed to read from local service")
		return
	}

	log.WithFields(logrus.Fields{
		"bytes": n,
	}).Debug("Received response from local service")

	// Send response back through WebSocket
	err = h.wsConn.WriteMessage(buf[:n])
	if err != nil {
		log.WithError(err).Error("Failed to send response")
		return
	}

	log.Debug("Forwarded response to client")
}

func (h *Handler) HandleHTTPRequest(w http.ResponseWriter, r *http.Request) {
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

	// Create HTTP client with proper timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

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

	// Copy response body with proper error handling
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		// Check if the error is due to chunked encoding
		if strings.Contains(err.Error(), "chunk length") {
			// Try to read the body in chunks
			buf := make([]byte, 4096)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					if _, writeErr := w.Write(buf[:n]); writeErr != nil {
						log.WithError(writeErr).Error("Failed to write response chunk")
						return
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					log.WithError(err).Error("Failed to read response chunk")
					return
				}
			}
		} else {
			log.WithError(err).Error("Failed to copy response body")
		}
	}
}

func (h *Handler) AssignPort() (int, error) {
	portMutex.Lock()
	defer portMutex.Unlock()

	port := nextPort
	for activePorts[port] {
		port++
	}
	nextPort = port + 1
	activePorts[port] = true

	h.wsConn.SetPort(port)
	return port, nil
}

func (h *Handler) ReleasePort(port int) {
	portMutex.Lock()
	defer portMutex.Unlock()
	delete(activePorts, port)
}

func (h *Handler) RegisterHost(host string, port int) {
	hostMap[host] = port
}

func (h *Handler) UnregisterHost(host string) {
	delete(hostMap, host)
}

func (h *Handler) Close() {
	h.cancel()
	if port := h.wsConn.GetPort(); port != 0 {
		h.ReleasePort(port)
	}
	if domain := h.wsConn.GetDomain(); domain != "" {
		h.UnregisterHost(domain)
	}
	h.wsConn.Close()
}
