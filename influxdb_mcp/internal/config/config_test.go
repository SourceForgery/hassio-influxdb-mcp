package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOptionsFileAndEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{
  "influx_url": "http://influx:8086",
  "influx_database": "ha",
  "influx_username": "reader",
  "influx_password": "secret",
  "influx_verify_tls": true,
  "request_timeout_seconds": 12,
  "max_points": 400,
  "max_entities_per_query": 8,
  "max_schema_series": 5000,
  "max_lookback_days": 365,
  "openai_tunnel_enabled": false,
  "openai_tunnel_id": "",
  "openai_tunnel_api_key": "",
  "tunnel_ready_timeout_seconds": 60,
  "log_level": "info"
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAX_POINTS", "123")
	t.Setenv("HTTP_LISTEN_ADDRESS", "127.0.0.1:0")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxPoints != 123 {
		t.Fatalf("MaxPoints = %d, want 123", cfg.MaxPoints)
	}
	if cfg.InfluxPassword != "secret" {
		t.Fatalf("password was not loaded")
	}
	if cfg.HTTPListenAddress != "127.0.0.1:0" {
		t.Fatalf("HTTPListenAddress = %q", cfg.HTTPListenAddress)
	}
}

func TestLoadRejectsUnknownOption(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "options.json")
	if err := os.WriteFile(path, []byte(`{"surprise": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load error = %v, want unknown field", err)
	}
}

func TestValidateRequiresTunnelCredentials(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.OpenAITunnelEnabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "openai_tunnel_id") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestValidateRejectsCredentialsInURL(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.InfluxURL = "http://user:pass@influx:8086"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted credentials embedded in URL")
	}
}
