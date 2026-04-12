package sodp

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	serverPingInterval = 30 * time.Second
	serverPongWait     = 60 * time.Second
	serverWriteWait    = 10 * time.Second

	maxFrameBytes = 64 * 1024  // 64 KiB max inbound frame — prevents OOM
	maxKeyLen     = 256        // max state key length
	maxValueBytes = 512 * 1024 // 512 KiB max value in a CALL mutation
	maxSessions   = 4096       // hard cap on concurrent WebSocket sessions
)

// Server is the SODP WebSocket server. It owns the state store and fanout bus,
// and manages client sessions. It replaces the old api.Hub.
type Server struct {
	State  *StateStore
	Fanout *FanoutBus

	mu           sync.RWMutex
	sessions     map[string]*Session
	sessionCount atomic.Int64

	serverID    string
	jwtSecret   []byte // HS256 secret; empty = auth disabled
	requireAuth bool   // when true, WATCH/CALL require authenticated session
	upgrader    websocket.Upgrader
}

// NewServer creates a new SODP server instance.
func NewServer() *Server {
	return &Server{
		State:    NewStateStore(),
		Fanout:   NewFanoutBus(),
		sessions: make(map[string]*Session),
		serverID: uuid.New().String()[:8],
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				allowed := os.Getenv("BROKOLI_CORS_ORIGINS")
				if allowed == "" || allowed == "*" {
					return true
				}
				for _, a := range strings.Split(allowed, ",") {
					if strings.TrimSpace(a) == origin {
						return true
					}
				}
				return false
			},
		},
	}
}

// SetJWTSecret configures HS256 JWT validation and enables auth requirement.
func (srv *Server) SetJWTSecret(secret []byte) {
	srv.jwtSecret = secret
	srv.requireAuth = len(secret) > 0
}

// HandleWS is the HTTP handler for WebSocket upgrade — drop-in replacement for Hub.HandleWS.
func (srv *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	// Connection limit
	if srv.sessionCount.Load() >= int64(maxSessions) {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	conn, err := srv.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("sodp: websocket upgrade error: %v", err)
		return
	}

	// Enforce max frame size on reads
	conn.SetReadLimit(maxFrameBytes)

	// Extract org_id from existing JWT middleware (if present)
	orgID := "default"
	preAuthed := false
	claims := r.Context().Value("claims")
	if claims != nil {
		if mc, ok := claims.(*jwt.MapClaims); ok {
			if id, ok := (*mc)["org_id"].(string); ok && id != "" {
				orgID = id
			}
			preAuthed = true // HTTP middleware already validated the JWT
		}
	}

	sess := NewSession(uuid.New().String(), orgID)
	if preAuthed {
		sess.MarkAuthenticated()
	}

	srv.mu.Lock()
	srv.sessions[sess.ID] = sess
	srv.mu.Unlock()
	srv.sessionCount.Add(1)

	// Configure keepalive
	conn.SetReadDeadline(time.Now().Add(serverPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(serverPongWait))
		return nil
	})

	// Send HELLO before starting pumps (only safe direct write).
	// "auth" tells @sodp/client whether to send an AUTH frame.
	if data, err := EncodeFrame(Frame{
		Type: FrameHello,
		Body: HelloBody{
			Protocol: "sodp",
			Version:  "0.1",
			ServerID: srv.serverID,
			Auth:     srv.requireAuth && !preAuthed,
		},
	}); err == nil {
		conn.SetWriteDeadline(time.Now().Add(serverWriteWait))
		conn.WriteMessage(websocket.BinaryMessage, data)
	}

	// Write pump — sole writer to conn after this point
	go srv.writePump(conn, sess)

	// Read pump (blocks until disconnect)
	srv.readPump(conn, sess)

	// Cleanup
	sess.Close()
	srv.Fanout.RemoveSession(sess.ID)
	srv.mu.Lock()
	delete(srv.sessions, sess.ID)
	srv.mu.Unlock()
	srv.sessionCount.Add(-1)
	conn.Close()
}

// writePump drains the session's Send channel and writes to the WebSocket.
func (srv *Server) writePump(conn *websocket.Conn, sess *Session) {
	ticker := time.NewTicker(serverPingInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-sess.Send:
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			conn.SetWriteDeadline(time.Now().Add(serverWriteWait))
			if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				return
			}
			// Drain queued messages in same write cycle
			n := len(sess.Send)
			for i := 0; i < n; i++ {
				if err := conn.WriteMessage(websocket.BinaryMessage, <-sess.Send); err != nil {
					return
				}
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(serverWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-sess.Done:
			return
		}
	}
}

// readPump reads inbound frames and dispatches them.
func (srv *Server) readPump(conn *websocket.Conn, sess *Session) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		frame, err := DecodeFrame(data)
		if err != nil {
			srv.sendError(sess, 0, 400, "invalid frame")
			continue
		}

		srv.dispatch(sess, frame)
	}
}

// dispatch routes an inbound frame to the appropriate handler.
func (srv *Server) dispatch(sess *Session, f Frame) {
	switch f.Type {
	case FrameAuth:
		srv.handleAuth(sess, f)
	case FrameWatch:
		if srv.requireAuthenticated(sess) {
			srv.handleWatch(sess, f)
		}
	case FrameUnwatch:
		srv.handleUnwatch(sess, f)
	case FrameCall:
		if srv.requireAuthenticated(sess) {
			srv.handleCall(sess, f)
		}
	case FrameResume:
		if srv.requireAuthenticated(sess) {
			srv.handleResume(sess, f)
		}
	case FrameHeartbeat:
		srv.sendFrame(sess, Frame{Type: FrameHeartbeat, Seq: f.Seq})
	case FrameAck:
		// no-op
	default:
		srv.sendError(sess, f.StreamID, 400, "unknown frame type")
	}
}

// requireAuthenticated checks that the session has been authenticated.
// Returns false and sends an error if auth is required but missing.
func (srv *Server) requireAuthenticated(sess *Session) bool {
	if !srv.requireAuth {
		return true // auth not configured — open mode
	}
	if sess.IsAuthenticated() {
		return true
	}
	srv.sendError(sess, 0, 401, "authentication required")
	return false
}

// handleAuth validates a JWT and stores claims on the session.
func (srv *Server) handleAuth(sess *Session, f Frame) {
	// Prevent re-auth (org_id escape)
	if sess.IsAuthenticated() {
		srv.sendError(sess, 0, 400, "already authenticated")
		return
	}

	if len(srv.jwtSecret) == 0 {
		sess.MarkAuthenticated()
		srv.sendFrame(sess, Frame{Type: FrameAuthOK, Body: AuthOKBody{Subject: "anonymous"}})
		return
	}

	body, err := decodeAuthBody(f.Body)
	if err != nil {
		srv.sendError(sess, 0, 400, "invalid auth body")
		return
	}

	token, err := jwt.Parse(body.Token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return srv.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		srv.sendError(sess, 0, 401, "invalid token")
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		srv.sendError(sess, 0, 401, "invalid claims")
		return
	}

	sub, _ := claims["sub"].(string)
	claimMap := make(map[string]any, len(claims))
	for k, v := range claims {
		claimMap[k] = v
	}
	sess.SetAuth(sub, claimMap)

	srv.sendFrame(sess, Frame{Type: FrameAuthOK, Body: AuthOKBody{Subject: sub}})
}

// handleWatch subscribes the session to a state key and sends STATE_INIT.
func (srv *Server) handleWatch(sess *Session, f Frame) {
	body, err := decodeWatchBody(f.Body)
	if err != nil {
		srv.sendError(sess, f.StreamID, 400, "invalid watch body")
		return
	}

	// Validate key
	if !isValidKey(body.Key) {
		srv.sendError(sess, f.StreamID, 400, "invalid key")
		return
	}

	// Tenant isolation: session can only watch keys in its own org namespace
	if !srv.keyAllowedForSession(sess, body.Key) {
		srv.sendError(sess, f.StreamID, 403, "access denied")
		return
	}

	streamID := f.StreamID
	if streamID == 0 {
		streamID = sess.AllocStream()
	}

	if !sess.AddWatch(streamID, body.Key) {
		srv.sendError(sess, f.StreamID, 429, "watch limit reached")
		return
	}

	srv.Fanout.Subscribe(body.Key, Subscriber{
		SessionID: sess.ID,
		StreamID:  streamID,
		OrgID:     sess.OrgID,
		Send:      sess.Send,
	})

	// Send STATE_INIT in @sodp/client format: { state, version, value, initialized }
	val, ver := srv.State.Get(body.Key)
	srv.sendFrame(sess, Frame{
		Type:     FrameStateInit,
		StreamID: streamID,
		Body: map[string]any{
			"state":       body.Key,
			"version":     ver,
			"value":       val,
			"initialized": val != nil,
		},
	})
}

// handleUnwatch removes a subscription.
func (srv *Server) handleUnwatch(sess *Session, f Frame) {
	key, ok := sess.RemoveWatch(f.StreamID)
	if ok {
		srv.Fanout.Unsubscribe(key, sess.ID, f.StreamID)
	}
}

// handleCall processes a state mutation.
// @sodp/client sends: { call_id, method, args: { state, value, patch, path } }
func (srv *Server) handleCall(sess *Session, f Frame) {
	if !sess.CheckRate() {
		srv.sendError(sess, f.StreamID, 429, "rate limit exceeded")
		return
	}

	body, err := decodeCallBody(f.Body)
	if err != nil {
		srv.sendError(sess, f.StreamID, 400, "invalid call body")
		return
	}

	key, _ := body.Args["state"].(string)
	if key == "" {
		srv.sendError(sess, f.StreamID, 400, "missing state key in args")
		return
	}

	if !isValidKey(key) {
		srv.sendError(sess, f.StreamID, 400, "invalid key")
		return
	}

	// Tenant isolation
	if !srv.keyAllowedForSession(sess, key) {
		srv.sendError(sess, f.StreamID, 403, "access denied")
		return
	}

	var delta *DeltaEntry

	switch body.Method {
	case "state.set":
		delta = srv.State.Apply(key, body.Args["value"])
	case "state.patch":
		current, _ := srv.State.Get(key)
		merged := mergeValues(current, body.Args["patch"])
		delta = srv.State.Apply(key, merged)
	case "state.set_in":
		path, _ := body.Args["path"].(string)
		if path == "" {
			srv.sendError(sess, f.StreamID, 400, "path required for set_in")
			return
		}
		current, _ := srv.State.Get(key)
		updated := setIn(current, path, body.Args["value"])
		delta = srv.State.Apply(key, updated)
	case "state.delete":
		delta = srv.State.Delete(key)
	case "state.presence":
		// TODO: session-scoped presence with auto-cleanup
		path, _ := body.Args["path"].(string)
		current, _ := srv.State.Get(key)
		updated := setIn(current, path, body.Args["value"])
		delta = srv.State.Apply(key, updated)
	default:
		srv.sendError(sess, f.StreamID, 400, "unknown method: "+body.Method)
		return
	}

	// RESULT in @sodp/client format: { call_id, success, data }
	resultData := map[string]any{}
	if delta != nil {
		resultData["version"] = delta.Version
	}
	srv.sendFrame(sess, Frame{
		Type:     FrameResult,
		StreamID: f.StreamID,
		Seq:      f.Seq,
		Body: map[string]any{
			"call_id": body.CallID,
			"success": true,
			"data":    resultData,
		},
	})

	if delta != nil {
		srv.Fanout.BroadcastAll(*delta, sess.ID, sess.OrgID)
	}
}

// handleResume replays missed deltas or falls back to STATE_INIT.
func (srv *Server) handleResume(sess *Session, f Frame) {
	body, err := decodeWatchBody(f.Body)
	if err != nil {
		srv.sendError(sess, f.StreamID, 400, "invalid resume body")
		return
	}

	if !isValidKey(body.Key) {
		srv.sendError(sess, f.StreamID, 400, "invalid key")
		return
	}

	if !srv.keyAllowedForSession(sess, body.Key) {
		srv.sendError(sess, f.StreamID, 403, "access denied")
		return
	}

	deltas := srv.State.DeltasSince(body.Key, body.SinceVersion)
	if deltas == nil {
		srv.handleWatch(sess, f)
		return
	}

	streamID := f.StreamID
	if streamID == 0 {
		streamID = sess.AllocStream()
	}

	if !sess.AddWatch(streamID, body.Key) {
		srv.sendError(sess, f.StreamID, 429, "watch limit reached")
		return
	}

	srv.Fanout.Subscribe(body.Key, Subscriber{
		SessionID: sess.ID,
		StreamID:  streamID,
		OrgID:     sess.OrgID,
		Send:      sess.Send,
	})

	for _, d := range deltas {
		frame, err := EncodeFrame(Frame{
			Type:     FrameDelta,
			StreamID: streamID,
			Seq:      d.Version,
			Body:     d,
		})
		if err != nil {
			continue
		}
		select {
		case sess.Send <- frame:
		default:
		}
	}
}

// Mutate is the server-side API for applying state changes from the engine.
// orgID is passed for tenant-scoped fanout filtering.
func (srv *Server) Mutate(key string, value any) {
	delta := srv.State.Apply(key, value)
	if delta != nil {
		orgID := extractOrgFromKey(key)
		srv.Fanout.BroadcastAll(*delta, "", orgID)
	}
}

// MutateAppend is the server-side API for appending to a slice state key.
// Uses O(1) delta encoding instead of full diff — critical for log fanout.
func (srv *Server) MutateAppend(key string, element any, maxLen int) {
	delta := srv.State.Append(key, element, maxLen)
	if delta != nil {
		orgID := extractOrgFromKey(key)
		srv.Fanout.BroadcastAll(*delta, "", orgID)
	}
}

// MutateDelete is the server-side API for removing state.
func (srv *Server) MutateDelete(key string) {
	delta := srv.State.Delete(key)
	if delta != nil {
		orgID := extractOrgFromKey(key)
		srv.Fanout.BroadcastAll(*delta, "", orgID)
	}
}

// SessionCount returns the number of active sessions.
func (srv *Server) SessionCount() int {
	return int(srv.sessionCount.Load())
}

// StartEviction launches a background goroutine that periodically removes
// state for completed runs older than ttl. Stops when done is closed.
func (srv *Server) StartEviction(ttl time.Duration, interval time.Duration, done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := srv.State.EvictCompleted(ttl)
				if n > 0 {
					log.Printf("sodp: evicted %d stale state keys", n)
				}
			case <-done:
				return
			}
		}
	}()
}

// --- security helpers ---

// keyAllowedForSession checks tenant isolation on key access.
// "default" org can access everything (community/single-tenant mode).
// Other orgs can only access keys prefixed with "runs." where the run's
// org_id matches, OR keys that have no org scope. We check by looking at
// the stored state's org_id field, or by prefix for known namespaces.
func (srv *Server) keyAllowedForSession(sess *Session, key string) bool {
	if sess.OrgID == "default" {
		return true // community mode — full access
	}
	// Event stream: org-scoped clients can only watch their own stream
	if strings.HasPrefix(key, "_events") {
		allowed := "_events." + sess.OrgID
		return key == allowed
	}
	// For the "runs." namespace, the run key encodes the run ID, and the
	// run state includes org_id. Check the stored org matches.
	if strings.HasPrefix(key, "runs.") {
		return srv.checkRunOrgAccess(sess.OrgID, key)
	}
	return true // non-namespaced keys are accessible
}

// checkRunOrgAccess verifies that a run key belongs to the session's org.
func (srv *Server) checkRunOrgAccess(sessionOrg, key string) bool {
	// Extract the run-level key: "runs.{run_id}" from "runs.{run_id}.nodes.x" etc.
	runKey := key
	parts := strings.SplitN(key, ".", 3)
	if len(parts) >= 2 {
		runKey = parts[0] + "." + parts[1]
	}

	val, _ := srv.State.Get(runKey)
	if val == nil {
		return true // key doesn't exist yet — allow (will be created by engine with correct org)
	}
	m, ok := val.(map[string]any)
	if !ok {
		return true
	}
	orgID, _ := m["org_id"].(string)
	return orgID == "" || orgID == sessionOrg
}

// isValidKey validates a state key.
func isValidKey(key string) bool {
	if key == "" || len(key) > maxKeyLen {
		return false
	}
	if !utf8.ValidString(key) {
		return false
	}
	// Must not contain control characters or path traversal
	for _, r := range key {
		if r < 0x20 || r == '\\' || r == '/' {
			return false
		}
	}
	// Must not start or end with a dot
	if key[0] == '.' || key[len(key)-1] == '.' {
		return false
	}
	// Must not have consecutive dots
	return !strings.Contains(key, "..")
}

// extractOrgFromKey tries to extract the org_id from a run's state.
// This is used for fanout filtering when the mutation comes from the engine.
func extractOrgFromKey(key string) string {
	// The bridge stores org_id in the run state — but for fanout we need
	// to know it at broadcast time. The bridge sets it on "runs.{id}" keys.
	// We return empty string (meaning "visible to all") by default.
	// Actual tenant filtering happens in FanoutBus.Broadcast via subscriber OrgID.
	return ""
}

// --- body decoders (zero-alloc: direct map access, no JSON round-trip) ---

func decodeAuthBody(body any) (AuthBody, error) {
	m, ok := body.(map[string]any)
	if !ok {
		return AuthBody{}, fmt.Errorf("expected map")
	}
	token, _ := m["token"].(string)
	if token == "" {
		return AuthBody{}, fmt.Errorf("missing token")
	}
	return AuthBody{Token: token}, nil
}

func decodeWatchBody(body any) (WatchBody, error) {
	m, ok := body.(map[string]any)
	if !ok {
		return WatchBody{}, fmt.Errorf("expected map")
	}
	// @sodp/client uses "state" field name
	key, _ := m["state"].(string)
	if key == "" {
		return WatchBody{}, fmt.Errorf("missing state")
	}
	var sinceVersion uint64
	switch v := m["since_version"].(type) {
	case uint64:
		sinceVersion = v
	case int64:
		sinceVersion = uint64(v)
	case float64:
		sinceVersion = uint64(v)
	}
	return WatchBody{Key: key, SinceVersion: sinceVersion}, nil
}

func decodeCallBody(body any) (CallBody, error) {
	m, ok := body.(map[string]any)
	if !ok {
		return CallBody{}, fmt.Errorf("expected map")
	}
	callID, _ := m["call_id"].(string)
	method, _ := m["method"].(string)
	if method == "" {
		return CallBody{}, fmt.Errorf("missing method")
	}
	args, _ := m["args"].(map[string]any)
	if args == nil {
		args = make(map[string]any)
	}
	return CallBody{CallID: callID, Method: method, Args: args}, nil
}

// --- frame helpers ---

// sendFrame encodes and enqueues a frame on the session's Send channel.
// The write pump is the sole goroutine that writes to the connection.
func (srv *Server) sendFrame(sess *Session, f Frame) {
	data, err := EncodeFrame(f)
	if err != nil {
		return
	}
	select {
	case sess.Send <- data:
	default:
		// Buffer full — drop (slow client)
	}
}

func (srv *Server) sendError(sess *Session, streamID uint32, code int, msg string) {
	srv.sendFrame(sess, Frame{
		Type:     FrameError,
		StreamID: streamID,
		Body:     ErrorBody{Code: code, Message: msg},
	})
}

// --- value helpers ---

// mergeValues does a shallow merge of patch into current (both map[string]any).
func mergeValues(current, patch any) any {
	cm, cOK := toStringMap(current)
	pm, pOK := toStringMap(patch)
	if !cOK || !pOK {
		return patch
	}
	merged := make(map[string]any, len(cm)+len(pm))
	for k, v := range cm {
		merged[k] = v
	}
	for k, v := range pm {
		merged[k] = v
	}
	return merged
}

// setIn sets a nested field within a map value using dot-separated path.
func setIn(current any, path string, value any) any {
	cm, ok := toStringMap(current)
	if !ok {
		cm = make(map[string]any)
	}
	result := make(map[string]any, len(cm))
	for k, v := range cm {
		result[k] = v
	}

	parts := strings.Split(path, ".")
	target := result
	for i, p := range parts {
		if i == len(parts)-1 {
			target[p] = value
			break
		}
		next, ok := toStringMap(target[p])
		if !ok {
			next = make(map[string]any)
		} else {
			cp := make(map[string]any, len(next))
			for k, v := range next {
				cp[k] = v
			}
			next = cp
		}
		target[p] = next
		target = next
	}
	return result
}
