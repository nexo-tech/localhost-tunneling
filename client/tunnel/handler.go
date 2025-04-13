package tunnel

import (
	"context"
	"net"

	"localhost-tunneling/client/ws"

	"github.com/sirupsen/logrus"
)

var log = logrus.New()

type Handler struct {
	wsConn    *ws.Connection
	localPort string
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewHandler(wsConn *ws.Connection, localPort string) *Handler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Handler{
		wsConn:    wsConn,
		localPort: localPort,
		ctx:       ctx,
		cancel:    cancel,
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
			if err := h.wsConn.Reconnect(); err != nil {
				log.WithError(err).Fatal("Failed to reconnect after retries")
			}
			// Restart the message reading goroutine
			go h.readMessages(messageChan, errorChan)

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
	}).Debug("Received message from server")

	// Connect to local service
	localConn, err := net.Dial("tcp", "localhost:"+h.localPort)
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

	log.Debug("Forwarded response to server")
}

func (h *Handler) Close() {
	h.cancel()
}
