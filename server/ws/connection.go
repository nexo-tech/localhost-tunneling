package ws

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

var (
	log      = logrus.New()
	upgrader = websocket.Upgrader{
		CheckOrigin:       func(r *http.Request) bool { return true },
		HandshakeTimeout:  10 * time.Second,
		EnableCompression: true,
	}
)

type Connection struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	port   int
	domain string
	mu     sync.Mutex
}

func NewConnection(w http.ResponseWriter, r *http.Request) (*Connection, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Connection{
		conn:   conn,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

func (c *Connection) SetPort(port int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.port = port
}

func (c *Connection) GetPort() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.port
}

func (c *Connection) SetDomain(domain string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.domain = domain
}

func (c *Connection) GetDomain() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.domain
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

func (c *Connection) WriteTextMessage(message string) error {
	return c.conn.WriteMessage(websocket.TextMessage, []byte(message))
}

func (c *Connection) Close() {
	c.cancel()
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Connection) Context() context.Context {
	return c.ctx
}
