package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/hc12r/broked/extensions"
	"github.com/hc12r/broked/models"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser clients
		}
		allowed := os.Getenv("BROKOLI_CORS_ORIGINS")
		if allowed == "" || allowed == "*" {
			return true // development mode
		}
		for _, a := range strings.Split(allowed, ",") {
			if strings.TrimSpace(a) == origin {
				return true
			}
		}
		return false
	},
}

const (
	pingInterval     = 30 * time.Second
	pongWait         = 60 * time.Second
	writeWait        = 10 * time.Second
	clientBufferSize = 64 // buffered messages per client
)

// wsClient wraps a WebSocket connection with a buffered send channel.
type wsClient struct {
	conn  *websocket.Conn
	send  chan []byte
	orgID string // tenant isolation — only receives events for this org
}

// Hub manages WebSocket connections and broadcasts events.
type Hub struct {
	clients  map[*wsClient]struct{}
	mu       sync.RWMutex
	eventBus extensions.EventBus // nil = local-only broadcast (open source default)
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*wsClient]struct{}),
	}
}

// SetEventBus wires a distributed event bus for cross-pod broadcasting.
// When set, StartBroadcasting will publish events to the bus and subscribe
// to events from other instances.
func (h *Hub) SetEventBus(eb extensions.EventBus) {
	h.eventBus = eb
}

// StartDistributedBroadcasting reads events from the engine, publishes them
// to the EventBus, and subscribes to the EventBus for events from other pods.
// Use this instead of StartBroadcasting when running in distributed mode.
func (h *Hub) StartDistributedBroadcasting(events <-chan models.Event) {
	// Forward local engine events to the event bus
	go func() {
		for event := range events {
			if h.eventBus == nil {
				continue
			}
			channel := "events:run"
			if event.OrgID != "" {
				channel = "events:org:" + event.OrgID
			}
			if data, err := json.Marshal(event); err == nil {
				h.eventBus.Publish(channel, data)
			}
		}
	}()

	// Subscribe to event bus and broadcast to local WebSocket clients
	if h.eventBus != nil {
		go func() {
			msgs, closer, err := h.eventBus.Subscribe("events:*")
			if err != nil {
				log.Printf("EventBus subscribe error: %v", err)
				return
			}
			defer closer()
			for msg := range msgs {
				var event models.Event
				if err := json.Unmarshal(msg.Data, &event); err == nil {
					h.Broadcast(event)
				}
			}
		}()
	}
}

// HandleWS upgrades HTTP to WebSocket and registers the client.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	// Extract org_id from JWT claims for tenant isolation
	orgID := "default"
	claims := r.Context().Value("claims")
	if claims != nil {
		if mc, ok := claims.(*jwt.MapClaims); ok {
			if id, ok := (*mc)["org_id"].(string); ok && id != "" {
				orgID = id
			}
		}
	}

	client := &wsClient{
		conn:  conn,
		send:  make(chan []byte, clientBufferSize),
		orgID: orgID,
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

// Broadcast sends an event to clients in the matching org only (tenant isolation).
func (h *Hub) Broadcast(event models.Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		// Tenant isolation: only send to clients in the same org
		// "default" org receives all events (single-tenant / community mode)
		if client.orgID != "default" && event.OrgID != "" && client.orgID != event.OrgID {
			continue
		}

		select {
		case client.send <- data:
			// Sent to buffer
		default:
			// Client buffer full — drop message (slow client)
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
