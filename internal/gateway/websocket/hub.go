package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// Heartbeat: server pings every 30s, closes after 3 missed pongs (90s).
	pingInterval   = 30 * time.Second
	pongWait       = 90 * time.Second
	maxConnsPerUser = 3

	// Message size limits.
	maxClientMsgSize = 4 * 1024  // 4 KB client -> server
	maxServerMsgSize = 64 * 1024 // 64 KB server -> client

	// Send channel buffer.
	sendBufSize = 64

	// WebSocket close codes.
	closeCodeAuthFailed = websocket.StatusCode(4001)
)

// ---------------------------------------------------------------------------
// JWT claims for WebSocket auth
// ---------------------------------------------------------------------------

// wsClaims mirrors middleware.Claims. JSON tags MUST match the token-generating
// handler.Claims: uid, tid, wid, role. (Previously these were user_id/tenant_id/
// workspace_id, which silently parsed as empty — breaking ALL WS tenant scoping,
// not just the conversation-scoped fan-out. Surfaced by the GAP-3b live test.)
type wsClaims struct {
	UserID      string `json:"uid"`
	TenantID    string `json:"tid"`
	WorkspaceID string `json:"wid"`
	Role        string `json:"role"`
	jwt.RegisteredClaims
}

// ---------------------------------------------------------------------------
// Wire message types (JSON over WebSocket)
// ---------------------------------------------------------------------------

type wsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type authPayload struct {
	Token string `json:"token"`
}

type conversationPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ---------------------------------------------------------------------------
// Hub — manages all connected WebSocket clients
// ---------------------------------------------------------------------------

// ConversationAuthorizer resolves whether a (role, userID) caller may access a
// conversation. Backed in production by the gateway handler's
// CanAccessConversation, which resolves the conversation's assigned_to via the
// inbox service and applies the cs_agent ownership model (own + unassigned).
// Injected so the hub can gate per-conversation subscriptions without importing
// the handler package (avoids an import cycle) and so tests can stub it.
type ConversationAuthorizer interface {
	CanAccessConversation(ctx context.Context, role, userID, conversationID string) bool
}

// Hub maintains the set of active WebSocket clients and broadcasts messages
// to clients scoped by tenant/workspace.
type Hub struct {
	clients   map[*Client]struct{}
	mu        sync.RWMutex
	jwtSecret []byte
	authz     ConversationAuthorizer // may be nil (tests / partial wiring)
	log       zerolog.Logger
}

// NewHub creates a new Hub with the given JWT secret for token validation.
// authz gates per-conversation subscriptions; when nil, subscription access
// checks are skipped (legacy behavior — used only in unit tests that don't
// exercise subscription authorization).
func NewHub(jwtSecret []byte, authz ConversationAuthorizer, log zerolog.Logger) *Hub {
	return &Hub{
		clients:   make(map[*Client]struct{}),
		jwtSecret: jwtSecret,
		authz:     authz,
		log:       log.With().Str("component", "ws-hub").Logger(),
	}
}

// ---------------------------------------------------------------------------
// Client — a single WebSocket connection
// ---------------------------------------------------------------------------

// Client represents a single connected WebSocket user session.
type Client struct {
	conn          *websocket.Conn
	hub           *Hub
	userID        string
	tenantID      string
	workspaceID   string
	role          string
	subscriptions map[string]bool // conversation_id -> subscribed
	send          chan []byte
	done          chan struct{}
}

// ---------------------------------------------------------------------------
// HTTP handler — WebSocket upgrade
// ---------------------------------------------------------------------------

// ServeHTTP upgrades an HTTP request to a WebSocket connection, authenticates
// via JWT, registers the client, and starts the read/write pumps.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract JWT token from query param or Authorization header.
	token := r.URL.Query().Get("token")
	if token == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	if token == "" {
		h.log.Warn().Str("remote", r.RemoteAddr).Msg("ws: missing token")
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	// Validate JWT.
	claims, err := h.parseToken(token)
	if err != nil {
		h.log.Warn().Err(err).Str("remote", r.RemoteAddr).Msg("ws: invalid token")
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// Enforce max connections per user.
	if h.countUserConns(claims.UserID) >= maxConnsPerUser {
		h.log.Warn().Str("user_id", claims.UserID).Msg("ws: max connections reached")
		http.Error(w, "max connections per user exceeded", http.StatusTooManyRequests)
		return
	}

	// Accept WebSocket upgrade.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allow all origins in dev; tighten in production via reverse proxy.
	})
	if err != nil {
		h.log.Error().Err(err).Str("remote", r.RemoteAddr).Msg("ws: accept failed")
		return
	}

	// Set read limit for client messages.
	conn.SetReadLimit(maxClientMsgSize)

	// Create client.
	c := &Client{
		conn:          conn,
		hub:           h,
		userID:        claims.UserID,
		tenantID:      claims.TenantID,
		workspaceID:   claims.WorkspaceID,
		role:          claims.Role,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, sendBufSize),
		done:          make(chan struct{}),
	}

	// Register with hub.
	h.register(c)

	h.log.Info().
		Str("user_id", c.userID).
		Str("tenant_id", c.tenantID).
		Str("workspace_id", c.workspaceID).
		Str("role", c.role).
		Msg("ws: client connected")

	// Send "connected" message.
	connected, _ := json.Marshal(wsMessage{
		Type: "connected",
		Payload: mustMarshalRaw(map[string]string{
			"user_id":      c.userID,
			"workspace_id": c.workspaceID,
			"tenant_id":    c.tenantID,
		}),
	})
	c.sendMsg(connected)

	// Start read and write pumps.
	go c.writePump()
	go c.readPump()
}

// ---------------------------------------------------------------------------
// Hub broadcast methods
// ---------------------------------------------------------------------------

// Broadcast sends data to all clients that match the given tenant and workspace.
// If workspaceID is empty, it broadcasts to all clients in the tenant.
func (h *Hub) Broadcast(tenantID, workspaceID string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.tenantID != tenantID {
			continue
		}
		if workspaceID != "" && c.workspaceID != workspaceID {
			continue
		}
		c.sendMsg(data)
	}
}

// BroadcastToUser sends data to all connections belonging to the specified user.
func (h *Hub) BroadcastToUser(userID string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.userID == userID {
			c.sendMsg(data)
		}
	}
}

// BroadcastToConversation sends data to all clients subscribed to the given
// conversation (used for typing indicators and per-conversation events).
func (h *Hub) BroadcastToConversation(conversationID string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.subscriptions[conversationID] {
			c.sendMsg(data)
		}
	}
}

// BroadcastConversationScoped fans out a message event to clients per the
// conversation ownership model:
//
//   - client must be in the event's tenant
//   - privileged roles (non cs_agent) receive it (tenant-wide)
//   - cs_agent receives it only when the conversation is unassigned AND the
//     client is in the conversation's workspace, OR it's assigned to that agent
//
// This mirrors the REST/gRPC conversation read gate so live updates never leak
// another agent's conversation. assignedTo == "" means unassigned.
func (h *Hub) BroadcastConversationScoped(tenantID, workspaceID, assignedTo string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.tenantID != tenantID {
			continue
		}
		if c.role == "cs_agent" {
			switch {
			case assignedTo == "" && c.workspaceID == workspaceID:
				// unassigned: any cs_agent in the workspace
			case assignedTo == c.userID:
				// own
			default:
				continue
			}
		}
		c.sendMsg(data)
	}
}

// ---------------------------------------------------------------------------
// Hub internal helpers
// ---------------------------------------------------------------------------

func (h *Hub) register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
}

func (h *Hub) countUserConns(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	count := 0
	for c := range h.clients {
		if c.userID == userID {
			count++
		}
	}
	return count
}

func (h *Hub) parseToken(tokenStr string) (*wsClaims, error) {
	claims := &wsClaims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return h.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// ---------------------------------------------------------------------------
// Client: sendMsg (non-blocking enqueue)
// ---------------------------------------------------------------------------

func (c *Client) sendMsg(data []byte) {
	select {
	case c.send <- data:
	default:
		// Client is too slow; drop the message to avoid blocking the hub.
		c.hub.log.Warn().Str("user_id", c.userID).Msg("ws: send buffer full, dropping message")
	}
}

// ---------------------------------------------------------------------------
// Client: readPump — reads messages from the WebSocket connection
// ---------------------------------------------------------------------------

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister(c)
		c.conn.Close(websocket.StatusNormalClosure, "bye")
		close(c.done)
		c.hub.log.Info().Str("user_id", c.userID).Msg("ws: client disconnected (read)")
	}()

	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			// Connection closed or read error.
			if websocket.CloseStatus(err) != -1 {
				c.hub.log.Debug().Str("user_id", c.userID).Int("code", int(websocket.CloseStatus(err))).Msg("ws: client closed")
			} else {
				c.hub.log.Warn().Err(err).Str("user_id", c.userID).Msg("ws: read error")
			}
			return
		}

		c.handleMessage(data)
	}
}

// handleMessage processes a single client-to-server message.
func (c *Client) handleMessage(data []byte) {
	var msg wsMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendError("INVALID_MESSAGE", "malformed JSON")
		return
	}

	switch msg.Type {
	case "ping":
		c.handlePing()

	case "auth":
		c.handleAuth(msg.Payload)

	case "subscribe_conversation":
		c.handleSubscribe(msg.Payload)

	case "unsubscribe_conversation":
		c.handleUnsubscribe(msg.Payload)

	default:
		c.sendError("INVALID_MESSAGE", "unknown message type: "+msg.Type)
	}
}

func (c *Client) handlePing() {
	resp, _ := json.Marshal(wsMessage{
		Type: "pong",
		Payload: mustMarshalRaw(map[string]string{
			"server_time": time.Now().UTC().Format(time.RFC3339),
		}),
	})
	c.sendMsg(resp)
}

func (c *Client) handleAuth(payload json.RawMessage) {
	var p authPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.Token == "" {
		c.sendError("AUTH_FAILED", "missing or invalid token payload")
		return
	}

	claims, err := c.hub.parseToken(p.Token)
	if err != nil {
		c.sendError("AUTH_FAILED", "invalid or expired token")
		// Close on auth failure per spec.
		c.conn.Close(closeCodeAuthFailed, "auth failed")
		return
	}

	// Update client identity.
	c.userID = claims.UserID
	c.tenantID = claims.TenantID
	c.workspaceID = claims.WorkspaceID
	c.role = claims.Role

	resp, _ := json.Marshal(wsMessage{Type: "auth_ok"})
	c.sendMsg(resp)

	c.hub.log.Info().Str("user_id", c.userID).Msg("ws: re-authenticated")
}

func (c *Client) handleSubscribe(payload json.RawMessage) {
	var p conversationPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.ConversationID == "" {
		c.sendError("INVALID_MESSAGE", "missing conversation_id")
		return
	}

	// RBAC scope: a cs_agent may only subscribe to conversations they may read
	// (own + unassigned). Privileged roles pass through. Mirrors the REST/gRPC
	// conversation ownership model so live updates can't leak another agent's
	// conversation. Fail-closed when the authorizer can't verify.
	if c.hub.authz != nil && !c.hub.authz.CanAccessConversation(context.Background(), c.role, c.userID, p.ConversationID) {
		c.sendError("FORBIDDEN", "conversation assigned to another agent")
		c.hub.log.Warn().Str("user_id", c.userID).Str("conversation_id", p.ConversationID).Msg("ws: subscribe denied")
		return
	}

	c.subscriptions[p.ConversationID] = true

	resp, _ := json.Marshal(wsMessage{
		Type: "subscribed",
		Payload: mustMarshalRaw(map[string]string{
			"conversation_id": p.ConversationID,
		}),
	})
	c.sendMsg(resp)

	c.hub.log.Debug().Str("user_id", c.userID).Str("conversation_id", p.ConversationID).Msg("ws: subscribed")
}

func (c *Client) handleUnsubscribe(payload json.RawMessage) {
	var p conversationPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.ConversationID == "" {
		c.sendError("INVALID_MESSAGE", "missing conversation_id")
		return
	}

	delete(c.subscriptions, p.ConversationID)

	c.hub.log.Debug().Str("user_id", c.userID).Str("conversation_id", p.ConversationID).Msg("ws: unsubscribed")
}

func (c *Client) sendError(code, message string) {
	resp, _ := json.Marshal(wsMessage{
		Type: "error",
		Payload: mustMarshalRaw(map[string]string{
			"code":    code,
			"message": message,
		}),
	})
	c.sendMsg(resp)
}

// ---------------------------------------------------------------------------
// Client: writePump — writes messages and handles heartbeat pings
// ---------------------------------------------------------------------------

func (c *Client) writePump() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	missedPongs := 0

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				// Hub closed our send channel.
				return
			}
			if err := c.conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
				c.hub.log.Warn().Err(err).Str("user_id", c.userID).Msg("ws: write error")
				return
			}

		case <-ticker.C:
			// Send WebSocket-level ping for heartbeat.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				missedPongs++
				c.hub.log.Debug().Str("user_id", c.userID).Int("missed", missedPongs).Msg("ws: pong missed")
				if missedPongs >= 3 {
					c.hub.log.Warn().Str("user_id", c.userID).Msg("ws: closing after 3 missed pongs")
					c.conn.Close(websocket.StatusGoingAway, "pong timeout")
					return
				}
			} else {
				missedPongs = 0
			}

		case <-c.done:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mustMarshalRaw marshals v to json.RawMessage, panicking on error (safe for
// static structures that are always valid JSON).
func mustMarshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic("websocket: mustMarshalRaw: " + err.Error())
	}
	return data
}
