package chat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"relay/internal/accounts"
	"relay/internal/model"
	"relay/internal/validation"
)

type Store interface {
	Insert(context.Context, string, string, string, string, string) (model.Message, error)
	Recent(context.Context, int) ([]model.Message, error)
}
type Authenticator interface {
	Authenticate(http.ResponseWriter, *http.Request) (accounts.Identity, error)
	ValidateSession(context.Context, string, string) error
}
type Config struct {
	HistoryLimit, MessageMaxRunes, MaxConnections int
	RoomName, SystemName, WelcomeMessage          string
	Rate                                          float64
	Burst                                         int
	PingInterval, SessionValidation               time.Duration
	Events                                        EventConfig
}
type EventConfig struct{ AnnounceJoins, AnnounceLeaves, AnnounceServerStart, AnnounceServerStop, Persist bool }

var errServerFull = errors.New("server connection limit reached")

type Module struct {
	store            Store
	auth             Authenticator
	cfg              Config
	log              *log.Logger
	mu               sync.Mutex
	clients          map[string]*client
	next             atomic.Uint64
	running          bool
	messagePublished func(model.Message)
}
type client struct {
	id          string
	user        model.PublicUser
	accessToken string
	expiresAt   time.Time
	conn        *websocket.Conn
	send        chan Event
	limiter     *limiter
}
type limiter struct {
	mu                  sync.Mutex
	tokens, rate, burst float64
	last                time.Time
}
type Event struct {
	Type            string             `json:"type"`
	ClientID        string             `json:"clientId,omitempty"`
	Message         *model.Message     `json:"message,omitempty"`
	History         []model.Message    `json:"history,omitempty"`
	Users           []model.PublicUser `json:"users,omitempty"`
	Room            string             `json:"room,omitempty"`
	Welcome         *model.Message     `json:"welcome,omitempty"`
	MessageMaxRunes int                `json:"messageMaxRunes,omitempty"`
	Code            string             `json:"code,omitempty"`
	Error           string             `json:"error,omitempty"`
}
type inbound struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	UserID      string `json:"userId,omitempty"`
}

func New(store Store, auth Authenticator, cfg Config, logger *log.Logger) *Module {
	if cfg.RoomName == "" {
		cfg.RoomName = "general"
	}
	if cfg.SystemName == "" {
		cfg.SystemName = "SERVER"
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 25 * time.Second
	}
	if cfg.SessionValidation <= 0 {
		cfg.SessionValidation = 5 * time.Minute
	}
	return &Module{store: store, auth: auth, cfg: cfg, log: logger, clients: map[string]*client{}}
}
func (m *Module) OnMessagePublished(fn func(model.Message)) { m.messagePublished = fn }
func (m *Module) Name() string                              { return "chat" }
func (m *Module) Dependencies() []string                    { return []string{"persistence", "accounts"} }
func (m *Module) Init(context.Context) error                { return nil }
func (m *Module) RegisterHTTP(*http.ServeMux)               {}
func (m *Module) RegisterWebSockets(mux *http.ServeMux)     { mux.HandleFunc("GET /ws", m.handleWebSocket) }
func (m *Module) Start(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = true
	return nil
}
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	m.running = false
	clients := make([]*client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*client{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *client) { defer wg.Done(); _ = c.conn.Close(websocket.StatusGoingAway, "server shutting down") }(c)
	}
	wg.Wait()
	return nil
}

func (m *Module) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()
	if !running {
		http.Error(w, "chat unavailable", 503)
		return
	}
	id, err := m.auth.Authenticate(w, r)
	if err != nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if id.Profile == nil {
		http.Error(w, "Relay profile required", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		m.log.Printf("ERROR websocket accept: %v", err)
		return
	}
	conn.SetReadLimit(16 * 1024)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer conn.CloseNow()
	c := &client{id: fmt.Sprintf("c-%d", m.next.Add(1)), user: id.Profile.PublicUser, accessToken: id.AccessToken, expiresAt: id.ExpiresAt, conn: conn, send: make(chan Event, 32), limiter: newLimiter(m.cfg.Rate, m.cfg.Burst)}
	first, err := m.join(ctx, c)
	if err != nil {
		m.writeError(ctx, conn, "server_full", "The server has reached its connection limit.")
		return
	}
	m.log.Printf("USER CONNECTED username=%s", logValue(c.user.Username))
	if first && m.cfg.Events.AnnounceJoins {
		if err := m.systemEvent(ctx, "user_join", fmt.Sprintf("%s joined the room.", c.user.DisplayName)); err != nil {
			m.log.Printf("ERROR user join event: %v", err)
		}
	}
	writeDone := make(chan struct{})
	go func() { defer close(writeDone); m.writePump(ctx, c) }()
	validateDone := make(chan struct{})
	go func() { defer close(validateDone); m.sessionPump(ctx, c, cancel) }()
	defer func() {
		last := m.leave(c)
		if last && m.cfg.Events.AnnounceLeaves {
			eventCtx, eventCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := m.systemEvent(eventCtx, "user_leave", fmt.Sprintf("%s left the room.", c.user.DisplayName)); err != nil {
				m.log.Printf("ERROR user leave event: %v", err)
			}
			eventCancel()
		}
		cancel()
		<-writeDone
		<-validateDone
		m.log.Printf("USER DISCONNECTED username=%s", logValue(c.user.Username))
	}()
	for {
		var in inbound
		if err := wsjson.Read(ctx, conn, &in); err != nil {
			return
		}
		if in.Type != "message" || in.Username != "" || in.DisplayName != "" || in.UserID != "" {
			m.enqueue(c, Event{Type: "error", Code: "invalid_event", Error: "Only message text may be supplied."})
			continue
		}
		if !c.limiter.Allow() {
			m.enqueue(c, Event{Type: "error", Code: "rate_limited", Error: "Too many messages."})
			continue
		}
		text, err := validation.PlainText(in.Text, "message", m.cfg.MessageMaxRunes)
		if err != nil {
			m.enqueue(c, Event{Type: "error", Code: "invalid_message", Error: err.Error()})
			continue
		}
		msg, err := m.store.Insert(ctx, "user", c.user.ID, c.user.Username, c.user.DisplayName, text)
		if err != nil {
			m.log.Printf("ERROR persist message: %v", err)
			m.enqueue(c, Event{Type: "error", Code: "storage_error", Error: "Message was not saved."})
			continue
		}
		m.broadcast(Event{Type: "message", Message: &msg})
	}
}
func (m *Module) join(ctx context.Context, c *client) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.MaxConnections > 0 && len(m.clients) >= m.cfg.MaxConnections {
		return false, errServerFull
	}
	history, err := m.store.Recent(ctx, m.cfg.HistoryLimit)
	if err != nil {
		return false, err
	}
	first := true
	for _, existing := range m.clients {
		if existing.user.ID == c.user.ID {
			first = false
			break
		}
	}
	m.clients[c.id] = c
	users := m.usersLocked()
	var welcome *model.Message
	if m.cfg.WelcomeMessage != "" {
		welcome = &model.Message{Kind: "system", Username: m.cfg.SystemName, DisplayName: m.cfg.SystemName, Text: m.cfg.WelcomeMessage, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	}
	c.send <- Event{Type: "welcome", ClientID: c.id, History: history, Users: users, Room: m.cfg.RoomName, Welcome: welcome, MessageMaxRunes: m.cfg.MessageMaxRunes}
	if first {
		m.broadcastLocked(Event{Type: "presence", Users: users})
	}
	return first, nil
}
func (m *Module) leave(c *client) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[c.id]; !ok {
		return false
	}
	delete(m.clients, c.id)
	for _, other := range m.clients {
		if other.user.ID == c.user.ID {
			return false
		}
	}
	m.broadcastLocked(Event{Type: "presence", Users: m.usersLocked()})
	return true
}
func (m *Module) usersLocked() []model.PublicUser {
	seen := map[string]model.PublicUser{}
	for _, c := range m.clients {
		seen[c.user.ID] = c.user
	}
	out := make([]model.PublicUser, 0, len(seen))
	for _, u := range seen {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName == out[j].DisplayName {
			return out[i].Username < out[j].Username
		}
		return out[i].DisplayName < out[j].DisplayName
	})
	return out
}
func (m *Module) broadcast(e Event) { m.mu.Lock(); defer m.mu.Unlock(); m.broadcastLocked(e) }
func (m *Module) broadcastLocked(e Event) {
	for _, c := range m.clients {
		select {
		case c.send <- e:
		default:
			go c.conn.Close(websocket.StatusPolicyViolation, "slow client")
		}
	}
}
func (m *Module) enqueue(c *client, e Event) {
	select {
	case c.send <- e:
	default:
		_ = c.conn.Close(websocket.StatusPolicyViolation, "slow client")
	}
}
func (m *Module) writePump(ctx context.Context, c *client) {
	tick := time.NewTicker(m.cfg.PingInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(wctx, c.conn, e)
			cancel()
			if err != nil {
				return
			}
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
func (m *Module) sessionPump(ctx context.Context, c *client, cancel context.CancelFunc) {
	tick := time.NewTicker(m.cfg.SessionValidation)
	defer tick.Stop()
	var expiry <-chan time.Time
	if !c.expiresAt.IsZero() {
		timer := time.NewTimer(time.Until(c.expiresAt))
		defer timer.Stop()
		expiry = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry:
			_ = c.conn.Close(websocket.StatusPolicyViolation, "session expired")
			cancel()
			return
		case <-tick.C:
			vctx, done := context.WithTimeout(ctx, 10*time.Second)
			err := m.auth.ValidateSession(vctx, c.accessToken, c.user.ID)
			done()
			if err != nil {
				_ = c.conn.Close(websocket.StatusPolicyViolation, "session invalid")
				cancel()
				return
			}
		}
	}
}
func (m *Module) writeError(ctx context.Context, c *websocket.Conn, code, msg string) {
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = wsjson.Write(wctx, c, Event{Type: "error", Code: code, Error: msg})
}

func (m *Module) Broadcast(ctx context.Context, text string) error {
	text, err := validation.PlainText(text, "message", m.cfg.MessageMaxRunes)
	if err != nil {
		return err
	}
	msg, err := m.store.Insert(ctx, "system", "", m.cfg.SystemName, m.cfg.SystemName, text)
	if err != nil {
		return err
	}
	m.broadcast(Event{Type: "message", Message: &msg})
	if m.messagePublished != nil {
		m.messagePublished(msg)
	}
	return nil
}

// PublishStored delivers a message that was persisted through the versioned
// HTTP API to clients still using the legacy global-lobby WebSocket.
func (m *Module) PublishStored(msg model.Message) {
	if msg.ConversationID == model.GlobalConversationID {
		m.broadcast(Event{Type: "message", Message: &msg})
	}
}
func (m *Module) ServerEvent(ctx context.Context, kind, text string) error {
	enabled := (kind == "server_start" && m.cfg.Events.AnnounceServerStart) || (kind == "server_stop" && m.cfg.Events.AnnounceServerStop)
	if !enabled {
		return nil
	}
	return m.systemEvent(ctx, kind, text)
}
func (m *Module) systemEvent(ctx context.Context, kind, text string) error {
	msg := model.Message{Kind: "system", Username: m.cfg.SystemName, DisplayName: m.cfg.SystemName, Text: text, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if m.cfg.Events.Persist {
		stored, err := m.store.Insert(ctx, "system", "", m.cfg.SystemName, m.cfg.SystemName, text)
		if err != nil {
			return err
		}
		msg = stored
	}
	m.log.Printf("EVENT type=%s detail=%s", kind, logValue(text))
	m.broadcast(Event{Type: "message", Message: &msg})
	if m.messagePublished != nil {
		m.messagePublished(msg)
	}
	return nil
}
func (m *Module) ProfileUpdated(user model.PublicUser) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, c := range m.clients {
		if c.user.ID == user.ID {
			c.user = user
			changed = true
		}
	}
	if changed {
		m.broadcastLocked(Event{Type: "presence", Users: m.usersLocked()})
	}
}
func (m *Module) EventStatus() EventConfig { return m.cfg.Events }
func (m *Module) UserCount() int           { m.mu.Lock(); defer m.mu.Unlock(); return len(m.clients) }
func (m *Module) Users() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	users := m.usersLocked()
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = fmt.Sprintf("%s (@%s)", u.DisplayName, u.Username)
	}
	return out
}
func newLimiter(rate float64, burst int) *limiter {
	return &limiter{tokens: float64(burst), rate: rate, burst: float64(burst), last: time.Now()}
}
func (l *limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
func logValue(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}
