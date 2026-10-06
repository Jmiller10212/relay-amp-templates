package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Module struct {
	started  time.Time
	ready    func() bool
	database func(context.Context) bool
	users    func() int
	modules  func() []string
}

func New(ready func() bool, database func(context.Context) bool, users func() int, modules func() []string) *Module {
	return &Module{started: time.Now(), ready: ready, database: database, users: users, modules: modules}
}
func (m *Module) Name() string                      { return "health" }
func (m *Module) Dependencies() []string            { return []string{"persistence"} }
func (m *Module) Init(context.Context) error        { return nil }
func (m *Module) RegisterWebSockets(*http.ServeMux) {}
func (m *Module) Start(context.Context) error       { return nil }
func (m *Module) Stop(context.Context) error        { return nil }
func (m *Module) RegisterHTTP(mux *http.ServeMux)   { mux.HandleFunc("GET /health", m.handle) }
func (m *Module) handle(w http.ResponseWriter, r *http.Request) {
	db := m.database(r.Context())
	ready := m.ready() && db
	status := "ok"
	code := http.StatusOK
	if !ready {
		status = "unavailable"
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "ready": ready, "version": Version, "uptimeSeconds": int64(time.Since(m.started).Seconds()), "modules": m.modules(), "database": map[bool]string{true: "ok", false: "unavailable"}[db], "connections": m.users()})
}

var Version = "dev"
