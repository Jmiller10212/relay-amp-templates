package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Modules struct {
	Persistence    bool `json:"persistence"`
	Accounts       bool `json:"accounts"`
	Realtime       bool `json:"realtime"`
	Friends        bool `json:"friends"`
	DirectMessages bool `json:"direct_messages"`
	Servers        bool `json:"servers"`
	Chat           bool `json:"chat"`
	Health         bool `json:"health"`
}

type Authentication struct {
	SupabaseURL              string `json:"supabase_url,omitempty"`
	SupabasePublishableKey   string `json:"supabase_publishable_key,omitempty"`
	RegistrationEnabled      bool   `json:"registration_enabled"`
	PublicBaseURL            string `json:"public_base_url"`
	CookieSecureMode         string `json:"cookie_secure_mode"`
	UsernameMinRunes         int    `json:"username_min_runes"`
	UsernameMaxRunes         int    `json:"username_max_runes"`
	DisplayNameMinRunes      int    `json:"display_name_min_runes"`
	DisplayNameMaxRunes      int    `json:"display_name_max_runes"`
	UsernameCooldownHours    int    `json:"username_cooldown_hours"`
	ReservationLifetimeHours int    `json:"reservation_lifetime_hours"`
	RecoveryGrantMinutes     int    `json:"recovery_grant_minutes"`
	RateLimitAttempts        int    `json:"rate_limit_attempts"`
	RateLimitWindowSeconds   int    `json:"rate_limit_window_seconds"`
	SessionValidationSeconds int    `json:"session_validation_seconds"`
}

type Events struct {
	AnnounceJoins       bool `json:"announce_joins"`
	AnnounceLeaves      bool `json:"announce_leaves"`
	AnnounceServerStart bool `json:"announce_server_start"`
	AnnounceServerStop  bool `json:"announce_server_stop"`
	Persist             bool `json:"persist"`
}

type Config struct {
	Listen                   string         `json:"listen"`
	Port                     int            `json:"port"`
	DataDir                  string         `json:"data_dir"`
	HistoryLimit             int            `json:"history_limit"`
	MaxStoredMessages        int            `json:"max_stored_messages"`
	MaxConnections           int            `json:"max_connections"`
	RoomName                 string         `json:"room_name"`
	SystemName               string         `json:"system_name"`
	WelcomeMessage           string         `json:"welcome_message"`
	AllowDuplicateNames      bool           `json:"allow_duplicate_names"`
	UsernameMaxRunes         int            `json:"username_max_runes"`
	MessageMaxRunes          int            `json:"message_max_runes"`
	RateLimitPerSecond       float64        `json:"rate_limit_per_second"`
	RateLimitBurst           int            `json:"rate_limit_burst"`
	JoinTimeoutSeconds       int            `json:"join_timeout_seconds"`
	WebSocketPingSeconds     int            `json:"websocket_ping_seconds"`
	ShutdownTimeoutSeconds   int            `json:"shutdown_timeout_seconds"`
	UserLookupsPerMinute     int            `json:"user_lookups_per_minute"`
	FriendMutationsPerMinute int            `json:"friend_mutations_per_minute"`
	ServerNameMaxRunes       int            `json:"server_name_max_runes"`
	MaxOwnedServers          int            `json:"max_owned_servers"`
	MaxServerMemberships     int            `json:"max_server_memberships"`
	ServerInvitesPerHour     int            `json:"server_invites_per_hour"`
	Events                   Events         `json:"events"`
	Authentication           Authentication `json:"authentication"`
	Modules                  Modules        `json:"modules"`
	SupabaseURL              string         `json:"-"`
	SupabasePublishableKey   string         `json:"-"`
	ConfigPath               string         `json:"-"`
	ShutdownTimeout          time.Duration  `json:"-"`
}

func Defaults() Config {
	return Config{
		Listen: "127.0.0.1", Port: 8080, DataDir: "./data", HistoryLimit: 100,
		MaxStoredMessages: 0, MaxConnections: 100, RoomName: "general", SystemName: "SERVER",
		WelcomeMessage: "Welcome to Relay.", AllowDuplicateNames: true,
		UsernameMaxRunes: 32, MessageMaxRunes: 2000, RateLimitPerSecond: 5,
		RateLimitBurst: 10, JoinTimeoutSeconds: 10, WebSocketPingSeconds: 25, ShutdownTimeoutSeconds: 10,
		UserLookupsPerMinute: 30, FriendMutationsPerMinute: 20,
		ServerNameMaxRunes: 100, MaxOwnedServers: 20, MaxServerMemberships: 100, ServerInvitesPerHour: 30,
		Events:         Events{AnnounceJoins: true, AnnounceLeaves: true, AnnounceServerStart: true, AnnounceServerStop: true},
		Authentication: Authentication{RegistrationEnabled: true, PublicBaseURL: "http://127.0.0.1:8080", CookieSecureMode: "auto", UsernameMinRunes: 3, UsernameMaxRunes: 32, DisplayNameMinRunes: 1, DisplayNameMaxRunes: 32, UsernameCooldownHours: 168, ReservationLifetimeHours: 24, RecoveryGrantMinutes: 15, RateLimitAttempts: 10, RateLimitWindowSeconds: 60, SessionValidationSeconds: 300},
		Modules:        Modules{Persistence: true, Accounts: true, Realtime: true, Friends: true, DirectMessages: true, Servers: true, Chat: true, Health: true},
	}
}

func Load(args []string) (Config, bool, error) {
	cfg := Defaults()
	configPath := ""
	showVersion := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--version":
			showVersion = true
		case args[i] == "--config" && i+1 < len(args):
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
		}
	}
	if showVersion {
		return cfg, true, nil
	}
	if configPath != "" {
		data, err := readOrCreate(configPath, cfg)
		if err != nil {
			return cfg, false, fmt.Errorf("read config: %w", err)
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, false, fmt.Errorf("parse config: %w", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err == nil {
			var authRaw map[string]json.RawMessage
			_ = json.Unmarshal(raw["authentication"], &authRaw)
			if _, hasNew := authRaw["display_name_max_runes"]; !hasNew {
				if _, hasOld := raw["username_max_runes"]; hasOld {
					cfg.Authentication.DisplayNameMaxRunes = cfg.UsernameMaxRunes
				}
			}
		}
		cfg.ConfigPath = configPath
	}

	fs := flag.NewFlagSet("relay-server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.String("config", configPath, "path to JSON configuration")
	dataDir := fs.String("data-dir", cfg.DataDir, "persistent data directory")
	listen := fs.String("listen", cfg.Listen, "listen address")
	port := fs.Int("port", cfg.Port, "TCP port")
	fs.Bool("version", false, "print version")
	if err := fs.Parse(args); err != nil {
		return cfg, false, err
	}
	cfg.DataDir, cfg.Listen, cfg.Port = *dataDir, *listen, *port
	cfg.SupabaseURL = strings.TrimRight(strings.TrimSpace(cfg.Authentication.SupabaseURL), "/")
	cfg.SupabasePublishableKey = strings.TrimSpace(cfg.Authentication.SupabasePublishableKey)
	if value := strings.TrimSpace(os.Getenv("RELAY_SUPABASE_URL")); value != "" {
		cfg.SupabaseURL = strings.TrimRight(value, "/")
	}
	if value := strings.TrimSpace(os.Getenv("RELAY_SUPABASE_PUBLISHABLE_KEY")); value != "" {
		cfg.SupabasePublishableKey = value
	}
	if cfg.ConfigPath != "" && !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(filepath.Dir(cfg.ConfigPath), cfg.DataDir)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, false, err
	}
	return cfg, false, nil
}

func readOrCreate(path string, defaults Config) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return data, err
	}

	data, err = json.MarshalIndent(defaults, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	if _, writeErr := f.Write(data); writeErr != nil {
		_ = f.Close()
		return nil, writeErr
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Config) Validate() error {
	switch {
	case c.Listen == "":
		return errors.New("listen address cannot be empty")
	case c.Port < 1 || c.Port > 65535:
		return errors.New("port must be between 1 and 65535")
	case c.DataDir == "":
		return errors.New("data_dir cannot be empty")
	case c.HistoryLimit < 1 || c.HistoryLimit > 1000:
		return errors.New("history_limit must be between 1 and 1000")
	case c.MaxStoredMessages < 0 || c.MaxStoredMessages > 1000000:
		return errors.New("max_stored_messages must be between 0 and 1000000")
	case c.MaxConnections < 1 || c.MaxConnections > 100000:
		return errors.New("max_connections must be between 1 and 100000")
	case !validLabel(c.RoomName, 1, 64):
		return errors.New("room_name must be 1 to 64 printable characters")
	case !validLabel(c.SystemName, 1, 32):
		return errors.New("system_name must be 1 to 32 printable characters")
	case !validLabel(c.WelcomeMessage, 0, 500):
		return errors.New("welcome_message must be at most 500 printable characters")
	case c.UsernameMaxRunes < 1 || c.UsernameMaxRunes > 128:
		return errors.New("username_max_runes must be between 1 and 128")
	case c.Authentication.UsernameMinRunes < 1 || c.Authentication.UsernameMinRunes > 32:
		return errors.New("authentication.username_min_runes must be between 1 and 32")
	case c.Authentication.UsernameMaxRunes < c.Authentication.UsernameMinRunes || c.Authentication.UsernameMaxRunes > 32:
		return errors.New("authentication.username_max_runes must be between username_min_runes and 32")
	case c.Authentication.DisplayNameMinRunes < 1 || c.Authentication.DisplayNameMinRunes > 128:
		return errors.New("authentication.display_name_min_runes must be between 1 and 128")
	case c.Authentication.DisplayNameMaxRunes < c.Authentication.DisplayNameMinRunes || c.Authentication.DisplayNameMaxRunes > 128:
		return errors.New("authentication.display_name_max_runes must be between display_name_min_runes and 128")
	case c.Authentication.CookieSecureMode != "auto" && c.Authentication.CookieSecureMode != "always" && c.Authentication.CookieSecureMode != "never":
		return errors.New("authentication.cookie_secure_mode must be auto, always, or never")
	case !validPublicBaseURL(c.Authentication.PublicBaseURL):
		return errors.New("authentication.public_base_url must be an http or https origin without a path, query, or fragment")
	case c.Authentication.UsernameCooldownHours < 0 || c.Authentication.UsernameCooldownHours > 87600:
		return errors.New("authentication.username_cooldown_hours must be between 0 and 87600")
	case c.Authentication.ReservationLifetimeHours < 1 || c.Authentication.ReservationLifetimeHours > 168:
		return errors.New("authentication.reservation_lifetime_hours must be between 1 and 168")
	case c.Authentication.RecoveryGrantMinutes < 1 || c.Authentication.RecoveryGrantMinutes > 60:
		return errors.New("authentication.recovery_grant_minutes must be between 1 and 60")
	case c.Authentication.RateLimitAttempts < 1 || c.Authentication.RateLimitAttempts > 1000:
		return errors.New("authentication.rate_limit_attempts must be between 1 and 1000")
	case c.Authentication.RateLimitWindowSeconds < 1 || c.Authentication.RateLimitWindowSeconds > 3600:
		return errors.New("authentication.rate_limit_window_seconds must be between 1 and 3600")
	case c.Authentication.SessionValidationSeconds < 15 || c.Authentication.SessionValidationSeconds > 3600:
		return errors.New("authentication.session_validation_seconds must be between 15 and 3600")
	case c.MessageMaxRunes < 1 || c.MessageMaxRunes > 10000:
		return errors.New("message_max_runes must be between 1 and 10000")
	case c.RateLimitPerSecond <= 0 || c.RateLimitPerSecond > 100:
		return errors.New("rate_limit_per_second must be greater than 0 and at most 100")
	case c.RateLimitBurst < 1 || c.RateLimitBurst > 1000:
		return errors.New("rate_limit_burst must be between 1 and 1000")
	case c.JoinTimeoutSeconds < 1 || c.JoinTimeoutSeconds > 120:
		return errors.New("join_timeout_seconds must be between 1 and 120")
	case c.WebSocketPingSeconds < 5 || c.WebSocketPingSeconds > 300:
		return errors.New("websocket_ping_seconds must be between 5 and 300")
	case c.ShutdownTimeoutSeconds < 1 || c.ShutdownTimeoutSeconds > 120:
		return errors.New("shutdown_timeout_seconds must be between 1 and 120")
	case c.UserLookupsPerMinute < 1 || c.UserLookupsPerMinute > 10000:
		return errors.New("user_lookups_per_minute must be between 1 and 10000")
	case c.FriendMutationsPerMinute < 1 || c.FriendMutationsPerMinute > 10000:
		return errors.New("friend_mutations_per_minute must be between 1 and 10000")
	case c.ServerNameMaxRunes < 1 || c.ServerNameMaxRunes > 100:
		return errors.New("server_name_max_runes must be between 1 and 100")
	case c.MaxOwnedServers < 1 || c.MaxOwnedServers > 1000:
		return errors.New("max_owned_servers must be between 1 and 1000")
	case c.MaxServerMemberships < 1 || c.MaxServerMemberships > 10000:
		return errors.New("max_server_memberships must be between 1 and 10000")
	case c.ServerInvitesPerHour < 1 || c.ServerInvitesPerHour > 10000:
		return errors.New("server_invites_per_hour must be between 1 and 10000")
	}
	if c.Modules.Accounts && (c.SupabaseURL == "" || c.SupabasePublishableKey == "") {
		return errors.New("accounts module requires RELAY_SUPABASE_URL and RELAY_SUPABASE_PUBLISHABLE_KEY")
	}
	c.ShutdownTimeout = time.Duration(c.ShutdownTimeoutSeconds) * time.Second
	return nil
}

func validPublicBaseURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func validLabel(value string, minRunes, maxRunes int) bool {
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	n := utf8.RuneCountInString(value)
	if n < minRunes || n > maxRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
