package realtime

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"relay/internal/accounts"
	"relay/internal/ids"
	"relay/internal/model"
)

type Authenticator interface {
	Authenticate(http.ResponseWriter, *http.Request) (accounts.Principal, error)
	ValidateSession(context.Context, string, string) error
}

type Config struct {
	MaxConnections    int
	PingInterval      time.Duration
	SessionValidation time.Duration
}

type Event struct {
	Version    int       `json:"version"`
	EventID    string    `json:"eventId"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurredAt"`
	Data       any       `json:"data,omitempty"`
}

type client struct {
	id          string
	user        model.PublicUser
	accessToken string
	expiresAt   time.Time
	conn        *websocket.Conn
	send        chan Event
}

type Module struct {
	auth    Authenticator
	cfg     Config
	log     *log.Logger
	mu      sync.Mutex
	clients map[string]*client
	byUser  map[string]map[string]*client
	next    atomic.Uint64
	running bool
}

func New(auth Authenticator, cfg Config, logger *log.Logger) *Module {
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 100
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 25 * time.Second
	}
	if cfg.SessionValidation <= 0 {
		cfg.SessionValidation = 5 * time.Minute
	}
	return &Module{auth: auth, cfg: cfg, log: logger, clients: map[string]*client{}, byUser: map[string]map[string]*client{}}
}

func (m *Module) Name() string                { return "realtime" }
func (m *Module) Dependencies() []string      { return []string{"accounts"} }
func (m *Module) Init(context.Context) error  { return nil }
func (m *Module) RegisterHTTP(*http.ServeMux) {}
func (m *Module) Start(context.Context) error {
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	return nil
}
func (m *Module) RegisterWebSockets(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/realtime", m.handle)
}

func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	m.running = false
	clients := make([]*client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*client{}
	m.byUser = map[string]map[string]*client{}
	m.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.Close(websocket.StatusGoingAway, "server stopping")
	}
	return nil
}

func (m *Module) handle(w http.ResponseWriter, r *http.Request) {
	principal, err := m.auth.Authenticate(w, r)
	if err != nil || principal.Profile == nil {
		status := http.StatusUnauthorized
		if accounts.IsProviderUnavailable(err) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, "authentication required", status)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	id := fmt.Sprintf("rt-%d", m.next.Add(1))
	c := &client{id: id, user: principal.Profile.PublicUser, accessToken: principal.AccessToken, expiresAt: principal.ExpiresAt, conn: conn, send: make(chan Event, 64)}
	first, ok := m.add(c)
	if !ok {
		_ = conn.Close(websocket.StatusTryAgainLater, "server full")
		return
	}
	m.log.Printf("INFO realtime connected id=%s user=%s", id, c.user.Username)
	if first {
		m.PublishAll("presence.changed", map[string]any{"user": c.user, "status": "online"})
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go m.writeLoop(ctx, cancel, c)
	m.enqueue(c, newEvent("realtime.ready", map[string]any{"connectionId": id, "serverTime": time.Now().UTC()}))
	for {
		var ignored map[string]any
		if err := wsjson.Read(ctx, conn, &ignored); err != nil {
			break
		}
		m.enqueue(c, newEvent("error", map[string]any{"code": "receive_only", "message": "Realtime sockets receive events; use the HTTP API for mutations."}))
	}
	last := m.remove(c)
	if last {
		m.PublishAll("presence.changed", map[string]any{"user": c.user, "status": "offline"})
	}
	m.log.Printf("INFO realtime disconnected id=%s user=%s", id, c.user.Username)
}

func (m *Module) add(c *client) (bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running || len(m.clients) >= m.cfg.MaxConnections {
		return false, false
	}
	first := len(m.byUser[c.user.ID]) == 0
	m.clients[c.id] = c
	if m.byUser[c.user.ID] == nil {
		m.byUser[c.user.ID] = map[string]*client{}
	}
	m.byUser[c.user.ID][c.id] = c
	return first, true
}

func (m *Module) remove(c *client) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.clients[c.id]; !exists {
		return false
	}
	delete(m.clients, c.id)
	delete(m.byUser[c.user.ID], c.id)
	last := len(m.byUser[c.user.ID]) == 0
	if last {
		delete(m.byUser, c.user.ID)
	}
	return last
}

func (m *Module) writeLoop(ctx context.Context, cancel context.CancelFunc, c *client) {
	ping := time.NewTicker(m.cfg.PingInterval)
	validate := time.NewTicker(m.cfg.SessionValidation)
	defer ping.Stop()
	defer validate.Stop()
	var expiry <-chan time.Time
	if !c.expiresAt.IsZero() {
		t := time.NewTimer(time.Until(c.expiresAt))
		defer t.Stop()
		expiry = t.C
	}
	for {
		select {
		case event := <-c.send:
			wctx, done := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(wctx, c.conn, event)
			done()
			if err != nil {
				cancel()
				return
			}
		case <-ping.C:
			pctx, done := context.WithTimeout(ctx, 5*time.Second)
			err := c.conn.Ping(pctx)
			done()
			if err != nil {
				cancel()
				return
			}
		case <-validate.C:
			vctx, done := context.WithTimeout(ctx, 10*time.Second)
			err := m.auth.ValidateSession(vctx, c.accessToken, c.user.ID)
			done()
			if err != nil {
				_ = c.conn.Close(websocket.StatusPolicyViolation, "session invalid")
				cancel()
				return
			}
		case <-expiry:
			_ = c.conn.Close(websocket.StatusPolicyViolation, "session expired")
			cancel()
			return
		case <-ctx.Done():
			return
		}
	}
}

func newEvent(kind string, data any) Event {
	return Event{Version: 1, EventID: ids.New(), Type: kind, OccurredAt: time.Now().UTC(), Data: data}
}

func (m *Module) enqueue(c *client, event Event) {
	select {
	case c.send <- event:
	default:
		go c.conn.Close(websocket.StatusPolicyViolation, "client too slow")
	}
}

func (m *Module) PublishAll(kind string, data any) {
	event := newEvent(kind, data)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		m.enqueue(c, event)
	}
}

func (m *Module) PublishUsers(userIDs []string, kind string, data any) {
	event := newEvent(kind, data)
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, userID := range userIDs {
		if seen[userID] {
			continue
		}
		seen[userID] = true
		for _, c := range m.byUser[userID] {
			m.enqueue(c, event)
		}
	}
}

func (m *Module) ProfileUpdated(user model.PublicUser) {
	m.mu.Lock()
	for _, c := range m.byUser[user.ID] {
		c.user = user
	}
	m.mu.Unlock()
	m.PublishAll("profile.updated", map[string]any{"user": user})
}

func (m *Module) OnlineUsers() []model.PublicUser {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.PublicUser, 0, len(m.byUser))
	for _, clients := range m.byUser {
		for _, c := range clients {
			out = append(out, c.user)
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

func (m *Module) UserCount() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.byUser) }

func (m *Module) IsOnline(userID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byUser[userID]) > 0
}

func (m *Module) Users() []string {
	users := m.OnlineUsers()
	out := make([]string, len(users))
	for i, user := range users {
		out[i] = fmt.Sprintf("%s (@%s)", user.DisplayName, user.Username)
	}
	return out
}
