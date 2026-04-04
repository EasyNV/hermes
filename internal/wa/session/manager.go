package session

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/wa/sender"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Manager manages whatsmeow client sessions in memory.
type Manager interface {
	Connect(ctx context.Context, waNumberID, phone, jid, proxyID string, proxy *ProxyConfig) (*Info, string, error)
	Disconnect(waNumberID string) error
	GetSession(waNumberID string) (*Info, bool)
	GetClient(waNumberID string) (sender.WaClient, bool)
	GetQRCode(waNumberID string) (qr string, expiresAt time.Time, isLinked bool, err error)
	PairPhone(ctx context.Context, waNumberID, phoneNumber string) (string, error)
	ListSessions() []*Info
	GetPodStats() PodStats
	Close()
}

// ProxyConfig holds proxy connection details.
type ProxyConfig struct {
	Host     string
	Port     int32
	Username string
	Password string
	Type     string // "socks5" or "http"
}

// Info represents a session's current state (returned by Manager methods).
type Info struct {
	WaNumberID    string
	JID           string
	Phone         string
	State         hermesv1.SessionState
	ProxyID       string
	ConnectedAt   time.Time
	MessagesSent  int64
	MessagesRecvd int64
	MemoryBytes   int64
	QRCode        string
	QRExpiresAt   time.Time
}

// PodStats contains aggregate statistics for this WA pod.
type PodStats struct {
	TotalSessions     int32
	ConnectedSessions int32
	MemoryBytes       int64
	CPUPercent        float32
	NatsConsumerLag   int64
}

// NumberUpdater is a narrow interface for updating wa_number rows.
// Defined here to avoid importing the handler package (circular dep).
// handler.PgStore satisfies this interface.
type NumberUpdater interface {
	SetWaNumberConnected(ctx context.Context, id, jid, podID string) error
	SetWaNumberDisconnected(ctx context.Context, id string) error
	SetWaNumberBanned(ctx context.Context, id string) error
	IncrementSentCount(ctx context.Context, id string) error
}

// managedSession holds the whatsmeow client and session metadata.
type managedSession struct {
	waNumberID    string
	jid           string
	phone         string
	proxyID       string
	state         hermesv1.SessionState
	connectedAt   time.Time
	messagesSent  atomic.Int64
	messagesRecvd atomic.Int64
	qrCode        string
	qrExpiresAt   time.Time
	client        *whatsmeow.Client
}

func (s *managedSession) toInfo() *Info {
	return &Info{
		WaNumberID:    s.waNumberID,
		JID:           s.jid,
		Phone:         s.phone,
		State:         s.state,
		ProxyID:       s.proxyID,
		ConnectedAt:   s.connectedAt,
		MessagesSent:  s.messagesSent.Load(),
		MessagesRecvd: s.messagesRecvd.Load(),
		QRCode:        s.qrCode,
		QRExpiresAt:   s.qrExpiresAt,
	}
}

// realManager is the production session manager backed by whatsmeow.
type realManager struct {
	mu        sync.RWMutex
	sessions  map[string]*managedSession
	container *sqlstore.Container
	podID     string
	log       zerolog.Logger
	updater   NumberUpdater
	eventPub  EventPublisher
	startedAt time.Time
}

// NewManager creates a production Manager backed by whatsmeow.
func NewManager(container *sqlstore.Container, podID string, updater NumberUpdater, eventPub EventPublisher, log zerolog.Logger) Manager {
	return &realManager{
		sessions:  make(map[string]*managedSession),
		container: container,
		podID:     podID,
		log:       log,
		updater:   updater,
		eventPub:  eventPub,
		startedAt: time.Now(),
	}
}

func (m *realManager) Connect(ctx context.Context, waNumberID, phone, jid, proxyID string, proxy *ProxyConfig) (*Info, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Already connected?
	if sess, ok := m.sessions[waNumberID]; ok {
		if sess.state == hermesv1.SessionState_SESSION_STATE_CONNECTED {
			return sess.toInfo(), "", nil
		}
		// Clean up old session before reconnecting.
		sess.client.Disconnect()
		delete(m.sessions, waNumberID)
	}

	// Get or create whatsmeow device store.
	var client *whatsmeow.Client

	if jid != "" {
		jidParsed, err := types.ParseJID(jid)
		if err == nil {
			device, _ := m.container.GetDevice(ctx, jidParsed)
			if device != nil {
				client = whatsmeow.NewClient(device, nil)
			}
		}
	}
	if client == nil {
		device := m.container.NewDevice()
		client = whatsmeow.NewClient(device, zerologWaLogger{m.log.With().Str("wa_number_id", waNumberID).Logger()})
	}

	// Configure proxy.
	if proxy != nil {
		proxyURL := buildProxyURL(proxy)
		if proxyURL != "" {
			client.SetProxyAddress(proxyURL)
		}
	}

	sess := &managedSession{
		waNumberID: waNumberID,
		phone:      phone,
		proxyID:    proxyID,
		state:      hermesv1.SessionState_SESSION_STATE_CONNECTING,
		client:     client,
	}
	m.sessions[waNumberID] = sess

	// Register event handler.
	client.AddEventHandler(m.makeEventHandler(waNumberID))

	// Determine if QR pairing is needed.
	if client.Store.ID == nil {
		// New device — need QR code.
		// Use background context — the QR session must outlive the gRPC request.
		// If we pass the request ctx, it gets cancelled when ConnectSession returns,
		// which kills the whatsmeow websocket and invalidates the QR code.
		qrChan, err := client.GetQRChannel(context.Background())
		if err != nil {
			delete(m.sessions, waNumberID)
			return nil, "", fmt.Errorf("getting QR channel: %w", err)
		}
		err = client.Connect()
		if err != nil {
			delete(m.sessions, waNumberID)
			return nil, "", fmt.Errorf("connecting: %w", err)
		}

		m.log.Info().
			Str("wa_number_id", waNumberID).
			Bool("connected_after_connect", client.IsConnected()).
			Msg("client.Connect() returned, waiting for QR")

		// Wait for first QR code.
		for evt := range qrChan {
			if evt.Event == "code" {
				m.log.Info().
					Str("wa_number_id", waNumberID).
					Int("code_len", len(evt.Code)).
					Bool("connected", client.IsConnected()).
					Msg("first QR code received")
				sess.state = hermesv1.SessionState_SESSION_STATE_QR_PENDING
				sess.qrCode = evt.Code
				sess.qrExpiresAt = time.Now().Add(60 * time.Second)

				// Continue consuming QR events in background.
				go m.handleQRFlow(waNumberID, qrChan)
				return sess.toInfo(), evt.Code, nil
			}
			if evt.Event == "success" {
				sess.state = hermesv1.SessionState_SESSION_STATE_CONNECTED
				sess.connectedAt = time.Now()
				if client.Store.ID != nil {
					sess.jid = client.Store.ID.String()
				}
				m.onConnected(ctx, sess)
				return sess.toInfo(), "", nil
			}
			if evt.Event == "timeout" || evt.Event == "error" {
				delete(m.sessions, waNumberID)
				return nil, "", fmt.Errorf("QR channel: %s", evt.Event)
			}
		}
		delete(m.sessions, waNumberID)
		return nil, "", fmt.Errorf("QR channel closed unexpectedly")
	}

	// Existing device — reconnect.
	err := client.Connect()
	if err != nil {
		delete(m.sessions, waNumberID)
		return nil, "", fmt.Errorf("connecting: %w", err)
	}
	sess.state = hermesv1.SessionState_SESSION_STATE_CONNECTED
	sess.connectedAt = time.Now()
	if client.Store.ID != nil {
		sess.jid = client.Store.ID.String()
	}
	m.onConnected(ctx, sess)
	return sess.toInfo(), "", nil
}

func (m *realManager) Disconnect(waNumberID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[waNumberID]
	if !ok {
		return fmt.Errorf("session not found: %s", waNumberID)
	}

	sess.client.Disconnect()
	sess.state = hermesv1.SessionState_SESSION_STATE_DISCONNECTED

	if m.updater != nil {
		_ = m.updater.SetWaNumberDisconnected(context.Background(), waNumberID)
	}
	if m.eventPub != nil {
		m.eventPub.PublishConnection(waNumberID, sess.jid, sess.phone, hermesv1.WaConnectionState_WA_CONNECTION_STATE_DISCONNECTED, m.podID, "manual disconnect")
	}

	delete(m.sessions, waNumberID)
	return nil
}

func (m *realManager) GetSession(waNumberID string) (*Info, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[waNumberID]
	if !ok {
		return nil, false
	}
	return sess.toInfo(), true
}

func (m *realManager) GetClient(waNumberID string) (sender.WaClient, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[waNumberID]
	if !ok {
		return nil, false
	}
	if sess.state != hermesv1.SessionState_SESSION_STATE_CONNECTED {
		return nil, false
	}
	return &waClientAdapter{client: sess.client, session: sess}, true
}

func (m *realManager) GetQRCode(waNumberID string) (string, time.Time, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[waNumberID]
	if !ok {
		return "", time.Time{}, false, fmt.Errorf("session not found")
	}
	if sess.state == hermesv1.SessionState_SESSION_STATE_CONNECTED {
		return "", time.Time{}, true, nil
	}
	if sess.state != hermesv1.SessionState_SESSION_STATE_QR_PENDING {
		return "", time.Time{}, false, fmt.Errorf("session not in QR pending state")
	}
	return sess.qrCode, sess.qrExpiresAt, false, nil
}

func (m *realManager) PairPhone(ctx context.Context, waNumberID, phoneNumber string) (string, error) {
	m.mu.RLock()
	sess, ok := m.sessions[waNumberID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("session not found: %s (call Connect first)", waNumberID)
	}

	m.log.Info().
		Str("wa_number_id", waNumberID).
		Str("state", sess.state.String()).
		Bool("connected", sess.client.IsConnected()).
		Bool("logged_in", sess.client.IsLoggedIn()).
		Msg("PairPhone: session state check")

	if !sess.client.IsConnected() {
		return "", fmt.Errorf("websocket not connected (state=%s)", sess.state)
	}

	code, err := sess.client.PairPhone(ctx, phoneNumber, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", fmt.Errorf("pair phone: %w", err)
	}

	m.log.Info().
		Str("wa_number_id", waNumberID).
		Str("phone", phoneNumber).
		Str("code", code).
		Msg("phone pairing code generated")

	return code, nil
}

func (m *realManager) ListSessions() []*Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Info, 0, len(m.sessions))
	for _, sess := range m.sessions {
		result = append(result, sess.toInfo())
	}
	return result
}

func (m *realManager) GetPodStats() PodStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	var connected int32
	for _, sess := range m.sessions {
		if sess.state == hermesv1.SessionState_SESSION_STATE_CONNECTED {
			connected++
		}
	}

	return PodStats{
		TotalSessions:     int32(len(m.sessions)),
		ConnectedSessions: connected,
		MemoryBytes:       int64(memStats.Alloc),
	}
}

func (m *realManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		sess.client.Disconnect()
		delete(m.sessions, id)
	}
}

// onConnected updates DB and publishes connection event.
func (m *realManager) onConnected(ctx context.Context, sess *managedSession) {
	if m.updater != nil {
		_ = m.updater.SetWaNumberConnected(ctx, sess.waNumberID, sess.jid, m.podID)
	}
	if m.eventPub != nil {
		m.eventPub.PublishConnection(sess.waNumberID, sess.jid, sess.phone, hermesv1.WaConnectionState_WA_CONNECTION_STATE_CONNECTED, m.podID, "")
	}
}

// handleQRFlow consumes remaining QR channel events in the background.
func (m *realManager) handleQRFlow(waNumberID string, qrChan <-chan whatsmeow.QRChannelItem) {
	for evt := range qrChan {
		m.mu.Lock()
		sess, ok := m.sessions[waNumberID]
		if !ok {
			m.mu.Unlock()
			return
		}
		switch evt.Event {
		case "code":
			sess.qrCode = evt.Code
			sess.qrExpiresAt = time.Now().Add(60 * time.Second)
			if m.eventPub != nil {
				m.eventPub.PublishConnection(waNumberID, "", sess.phone, hermesv1.WaConnectionState_WA_CONNECTION_STATE_QR_READY, m.podID, "")
			}
		case "success":
			sess.state = hermesv1.SessionState_SESSION_STATE_CONNECTED
			sess.connectedAt = time.Now()
			if sess.client.Store.ID != nil {
				sess.jid = sess.client.Store.ID.String()
			}
			sess.qrCode = ""
			m.onConnected(context.Background(), sess)
		case "timeout", "error":
			sess.state = hermesv1.SessionState_SESSION_STATE_DISCONNECTED
			delete(m.sessions, waNumberID)
			m.log.Warn().Str("wa_number_id", waNumberID).Str("event", evt.Event).Msg("QR flow ended")
		}
		m.mu.Unlock()
	}
}

// buildProxyURL constructs a proxy URL from config.
func buildProxyURL(p *ProxyConfig) string {
	if p == nil || p.Host == "" {
		return ""
	}
	scheme := "socks5"
	if p.Type == "http" {
		scheme = "http"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", p.Host, p.Port),
	}
	if p.Username != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

// zerologWaLogger adapts zerolog to whatsmeow's Logger interface.
type zerologWaLogger struct {
	log zerolog.Logger
}

func (l zerologWaLogger) Warnf(msg string, args ...interface{})  { l.log.Warn().Msgf(msg, args...) }
func (l zerologWaLogger) Errorf(msg string, args ...interface{}) { l.log.Error().Msgf(msg, args...) }
func (l zerologWaLogger) Infof(msg string, args ...interface{})  { l.log.Info().Msgf(msg, args...) }
func (l zerologWaLogger) Debugf(msg string, args ...interface{}) { l.log.Debug().Msgf(msg, args...) }
func (l zerologWaLogger) Sub(module string) waLog.Logger {
	return zerologWaLogger{l.log.With().Str("wm", module).Logger()}
}

// waClientAdapter wraps *whatsmeow.Client to implement sender.WaClient.
type waClientAdapter struct {
	client  *whatsmeow.Client
	session *managedSession
}

func (a *waClientAdapter) SendMsg(ctx context.Context, recipientJID string, contentType hermesv1.ContentType, body, mediaURL, _, caption string) (string, time.Time, error) {
	jid, err := types.ParseJID(recipientJID)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parsing recipient JID: %w", err)
	}

	var msg *waE2E.Message
	switch contentType {
	case hermesv1.ContentType_CONTENT_TYPE_TEXT:
		msg = &waE2E.Message{
			Conversation: proto.String(body),
		}
	case hermesv1.ContentType_CONTENT_TYPE_IMAGE:
		msg = &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Caption: proto.String(caption),
				URL:     proto.String(mediaURL),
			},
		}
	case hermesv1.ContentType_CONTENT_TYPE_DOCUMENT:
		msg = &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				Caption: proto.String(caption),
				URL:     proto.String(mediaURL),
			},
		}
	case hermesv1.ContentType_CONTENT_TYPE_AUDIO:
		msg = &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				URL: proto.String(mediaURL),
			},
		}
	case hermesv1.ContentType_CONTENT_TYPE_VIDEO:
		msg = &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				Caption: proto.String(caption),
				URL:     proto.String(mediaURL),
			},
		}
	default:
		msg = &waE2E.Message{
			Conversation: proto.String(body),
		}
	}

	resp, err := a.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sending message: %w", err)
	}

	a.session.messagesSent.Add(1)
	return resp.ID, resp.Timestamp, nil
}

func (a *waClientAdapter) SendPresence(recipientJID string, composing bool) error {
	jid, err := types.ParseJID(recipientJID)
	if err != nil {
		return fmt.Errorf("parsing JID: %w", err)
	}

	state := types.ChatPresencePaused
	if composing {
		state = types.ChatPresenceComposing
	}
	return a.client.SendChatPresence(context.Background(), jid, state, types.ChatPresenceMedia(""))
}
