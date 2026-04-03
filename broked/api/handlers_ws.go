package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hc12r/broked/models"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

const (
	pingInterval     = 30 * time.Second
	pongWait         = 60 * time.Second
	writeWait        = 10 * time.Second
	clientBufferSize = 64 // buffered messages per client
)

// wsClient wraps a WebSocket connection with a buffered send channel.
type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub manages WebSocket connections and broadcasts events.
type Hub struct {
	clients map[*wsClient]struct{}
	mu      sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*wsClient]struct{}),
	}
}

// HandleWS upgrades HTTP to WebSocket and registers the client.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &wsClient{
		conn: conn,
		send: make(chan []byte, clientBufferSize),
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	// Write pump — drains the send channel, writes to WebSocket
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer func() {
			ticker.Stop()
			h.mu.Lock()
			delete(h.clients, client)
			h.mu.Unlock()
			conn.Close()
		}()

		for {
			select {
			case msg, ok := <-client.send:
				if !ok {
					conn.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
				// Drain any queued messages in the same write cycle
				n := len(client.send)
				for i := 0; i < n; i++ {
					msg = <-client.send
					if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
						return
					}
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Read pump — keep connection alive, handle close
	go func() {
		defer func() {
			close(client.send)
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

// Broadcast sends an event to all connected clients (non-blocking).
func (h *Hub) Broadcast(event models.Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		select {
		case client.send <- data:
			// Sent to buffer
		default:
			// Client buffer full — drop message (slow client)
			// The write pump will eventually close this client
		}
	}
}

// StartBroadcasting reads events from the engine and broadcasts them.
func (h *Hub) StartBroadcasting(events <-chan models.Event) {
	go func() {
		for event := range events {
			h.Broadcast(event)
		}
	}()
}
