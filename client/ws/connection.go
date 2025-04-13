package ws

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

var log = logrus.New()

type Connection struct {
	conn       *websocket.Conn
	serverIP   string
	serverPort string
	localPort  string
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewConnection(serverIP, serverPort, localPort string) *Connection {
	ctx, cancel := context.WithCancel(context.Background())
	return &Connection{
		serverIP:   serverIP,
		serverPort: serverPort,
		localPort:  localPort,
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (c *Connection) Connect() error {
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
	if c.serverPort == "443" {
		wsURL = fmt.Sprintf("wss://%s/tunnel", c.serverIP)
	} else {
		wsURL = fmt.Sprintf("wss://%s:%s/tunnel", c.serverIP, c.serverPort)
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
		return err
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

	c.conn = conn
	return nil
}

func (c *Connection) SendLocalPort() error {
	err := c.conn.WriteMessage(websocket.TextMessage, []byte(c.localPort))
	if err != nil {
		return err
	}
	log.WithField("local_port", c.localPort).Info("Sent local port to server")
	return nil
}

func (c *Connection) WaitForDomain() (string, error) {
	messageType, message, err := c.conn.ReadMessage()
	if err != nil {
		return "", err
	}
	if messageType != websocket.TextMessage {
		return "", fmt.Errorf("expected text message for domain assignment, got %d", messageType)
	}
	publicDomain := string(message)
	log.WithField("public_domain", publicDomain).Info("Received public domain assignment")
	return publicDomain, nil
}

func (c *Connection) StartPingTicker() {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	go func() {
		for {
			select {
			case <-pingTicker.C:
				if err := c.conn.WriteMessage(websocket.PingMessage, []byte("ping")); err != nil {
					log.WithError(err).Error("Failed to send ping")
					c.cancel()
					return
				}
			case <-c.ctx.Done():
				return
			}
		}
	}()
}

func (c *Connection) ReadMessage() ([]byte, error) {
	messageType, message, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if messageType != websocket.BinaryMessage {
		return nil, nil
	}
	return message, nil
}

func (c *Connection) WriteMessage(message []byte) error {
	return c.conn.WriteMessage(websocket.BinaryMessage, message)
}

func (c *Connection) Close() {
	c.cancel()
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Connection) Reconnect() error {
	var err error
	// Retry connection with exponential backoff
	for i := 0; i < 5; i++ {
		err = c.Connect()
		if err == nil {
			err = c.SendLocalPort()
			if err == nil {
				_, err = c.WaitForDomain()
				if err == nil {
					break
				}
			}
		}
		waitTime := time.Duration(1<<uint(i)) * time.Second
		log.WithFields(logrus.Fields{
			"attempt": i + 1,
			"wait":    waitTime,
			"error":   err,
		}).Error("Failed to reconnect, retrying...")
		time.Sleep(waitTime)
	}
	return err
}
