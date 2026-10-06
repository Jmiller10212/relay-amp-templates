package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigAndCLIOverrides(t *testing.T) {
	t.Setenv("RELAY_SUPABASE_URL", "https://example.supabase.co")
	t.Setenv("RELAY_SUPABASE_PUBLISHABLE_KEY", "test-publishable")
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.json")
	data := `{"listen":"127.0.0.1","port":9000,"data_dir":"state","history_limit":50,"username_max_runes":32,"message_max_runes":2000,"rate_limit_per_second":5,"rate_limit_burst":10,"shutdown_timeout_seconds":4,"modules":{"persistence":true,"chat":true,"health":true}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, version, err := Load([]string{"--config", path, "--port", "9100"})
	if err != nil {
		t.Fatal(err)
	}
	if version || cfg.Port != 9100 || cfg.HistoryLimit != 50 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if want := filepath.Join(dir, "state"); cfg.DataDir != want {
		t.Fatalf("data dir %q want %q", cfg.DataDir, want)
	}
	if cfg.Authentication.DisplayNameMaxRunes != 32 {
		t.Fatalf("legacy username limit was not mapped: %+v", cfg.Authentication)
	}
	if !cfg.Modules.DirectMessages {
		t.Fatal("old configuration did not inherit the direct_messages default")
	}
}

func TestLoadCreatesMissingConfig(t *testing.T) {
	t.Setenv("RELAY_SUPABASE_URL", "https://example.supabase.co")
	t.Setenv("RELAY_SUPABASE_PUBLISHABLE_KEY", "test-publishable")
	path := filepath.Join(t.TempDir(), "nested", "relay.json")

	cfg, version, err := Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if version {
		t.Fatal("unexpected version mode")
	}
	if cfg.Port != Defaults().Port {
		t.Fatalf("port = %d, want %d", cfg.Port, Defaults().Port)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %o, want no group/other access", info.Mode().Perm())
	}
	if _, _, err := Load([]string{"--config", path, "--port", "9101"}); err != nil {
		t.Fatalf("reloading generated config: %v", err)
	}
}

func TestEventAndServerDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.RoomName != "general" || cfg.SystemName != "SERVER" || cfg.MaxConnections != 100 {
		t.Fatalf("unexpected chat defaults: %+v", cfg)
	}
	if !cfg.Events.AnnounceJoins || !cfg.Events.AnnounceLeaves || !cfg.Events.AnnounceServerStart || !cfg.Events.AnnounceServerStop || cfg.Events.Persist {
		t.Fatalf("unexpected event defaults: %+v", cfg.Events)
	}
	bad := cfg
	bad.RoomName = "bad\nroom"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected control character validation error")
	}
	bad = cfg
	bad.WebSocketPingSeconds = 2
	if err := bad.Validate(); err == nil {
		t.Fatal("expected ping interval validation error")
	}
}
func TestRejectUnknownConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(p, []byte(`{"unknown":true}`), 0o600)
	if _, _, err := Load([]string{"--config", p}); err == nil {
		t.Fatal("expected error")
	}
}
func TestRejectInvalidPort(t *testing.T) {
	c := Defaults()
	c.Port = 0
	if err := c.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestRejectPublicBaseURLWithPath(t *testing.T) {
	c := Defaults()
	c.Authentication.PublicBaseURL = "https://relay.example/chat"
	if err := c.Validate(); err == nil {
		t.Fatal("expected invalid public base URL")
	}
}

func TestLoadSupabaseJSONFallbackAndEnvironmentPrecedence(t *testing.T) {
	t.Setenv("RELAY_SUPABASE_URL", "")
	t.Setenv("RELAY_SUPABASE_PUBLISHABLE_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.json")
	data := `{
  "authentication": {
    "supabase_url": "https://json-project.supabase.co/",
    "supabase_publishable_key": "json-publishable"
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SupabaseURL != "https://json-project.supabase.co" || cfg.SupabasePublishableKey != "json-publishable" {
		t.Fatalf("unexpected JSON provider settings: %q %q", cfg.SupabaseURL, cfg.SupabasePublishableKey)
	}

	t.Setenv("RELAY_SUPABASE_URL", "https://env-project.supabase.co/")
	t.Setenv("RELAY_SUPABASE_PUBLISHABLE_KEY", "env-publishable")
	cfg, _, err = Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SupabaseURL != "https://env-project.supabase.co" || cfg.SupabasePublishableKey != "env-publishable" {
		t.Fatalf("environment did not override JSON: %q %q", cfg.SupabaseURL, cfg.SupabasePublishableKey)
	}
}
