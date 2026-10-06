package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"relay/internal/config"
	"relay/internal/health"
	"relay/internal/modules/all"
	"relay/internal/webui"
)

var Version = "0.7.0-dev"

type App struct {
	cfg     config.Config
	logger  *log.Logger
	catalog *all.Catalog
	server  *http.Server
	started time.Time
	stop    chan string
	once    sync.Once
}

func New(cfg config.Config, logger *log.Logger) (*App, error) {
	cat, err := all.Build(cfg, logger)
	if err != nil {
		return nil, err
	}
	return &App{cfg: cfg, logger: logger, catalog: cat, started: time.Now(), stop: make(chan string, 1)}, nil
}

func (a *App) Run(ctx context.Context, input io.Reader) error {
	health.Version = Version
	if err := a.catalog.Registry.Init(ctx); err != nil {
		return err
	}
	mux := http.NewServeMux()
	a.catalog.Registry.Register(mux)
	mux.Handle("GET /", webui.Handler())
	a.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	if err := a.catalog.Registry.Start(ctx); err != nil {
		_ = a.catalog.Registry.Stop(context.Background())
		return err
	}
	addr := net.JoinHostPort(a.cfg.Listen, fmt.Sprintf("%d", a.cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = a.catalog.Registry.Stop(context.Background())
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	a.catalog.Ready.Store(true)
	fmt.Printf("RELAY READY address=%s version=%s\n", ln.Addr().String(), Version)
	a.logger.Printf("INFO server started modules=%s", strings.Join(a.catalog.Registry.Names(), ","))
	if a.cfg.Modules.Chat {
		if err := a.catalog.Chat.ServerEvent(ctx, "server_start", "Server started."); err != nil {
			a.logger.Printf("ERROR server start event: %v", err)
		}
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve(ln) }()
	if input != nil {
		go a.readConsole(input)
	}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	var reason string
	select {
	case reason = <-a.stop:
	case s := <-sig:
		reason = s.String()
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			reason = "server error"
			a.logger.Printf("ERROR HTTP server: %v", err)
		} else {
			reason = "server closed"
		}
	case <-ctx.Done():
		reason = ctx.Err().Error()
	}
	a.catalog.Ready.Store(false)
	a.logger.Printf("INFO shutdown requested reason=%s", reason)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()
	if a.cfg.Modules.Chat {
		if err := a.catalog.Chat.ServerEvent(shutdownCtx, "server_stop", "Server is stopping."); err != nil {
			a.logger.Printf("ERROR server stop event: %v", err)
		}
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- a.server.Shutdown(shutdownCtx) }()
	moduleErr := a.catalog.Registry.Stop(shutdownCtx)
	serverErr := <-serverDone
	if serverErr != nil {
		return serverErr
	}
	if moduleErr != nil {
		return moduleErr
	}
	a.logger.Printf("INFO shutdown complete")
	return nil
}

func (a *App) RequestStop(reason string) { a.once.Do(func() { a.stop <- reason }) }
func (a *App) readConsole(r io.Reader) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1024), 16*1024)
	for s.Scan() {
		a.ExecuteCommand(context.Background(), s.Text())
	}
	if err := s.Err(); err != nil {
		a.logger.Printf("ERROR console: %v", err)
	}
}
func (a *App) ExecuteCommand(ctx context.Context, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	cmd, arg, _ := strings.Cut(line, " ")
	cmd = strings.ToLower(cmd)
	arg = strings.TrimSpace(arg)
	switch cmd {
	case "help":
		fmt.Println("COMMANDS help | status | users | events | broadcast <message> | stop")
	case "status":
		fmt.Printf("STATUS ready=%t users=%d uptime=%s modules=%s\n", a.catalog.Ready.Load(), a.catalog.Realtime.UserCount(), time.Since(a.started).Round(time.Second), strings.Join(a.catalog.Registry.Names(), ","))
	case "users":
		u := a.catalog.Realtime.Users()
		if len(u) == 0 {
			fmt.Println("USERS none")
		} else {
			fmt.Printf("USERS count=%d names=%s\n", len(u), strings.Join(u, ", "))
		}
	case "events":
		e := a.catalog.Chat.EventStatus()
		fmt.Printf("EVENTS joins=%t leaves=%t server_start=%t server_stop=%t persist=%t\n", e.AnnounceJoins, e.AnnounceLeaves, e.AnnounceServerStart, e.AnnounceServerStop, e.Persist)
	case "broadcast":
		if arg == "" {
			fmt.Println("ERROR usage: broadcast <message>")
			return
		}
		if err := a.catalog.Chat.Broadcast(ctx, arg); err != nil {
			fmt.Printf("ERROR broadcast failed: %v\n", err)
		} else {
			fmt.Println("BROADCAST sent")
		}
	case "stop":
		fmt.Println("STOP requested")
		a.RequestStop("console")
	default:
		fmt.Printf("ERROR unknown command %q; type help\n", cmd)
	}
}
