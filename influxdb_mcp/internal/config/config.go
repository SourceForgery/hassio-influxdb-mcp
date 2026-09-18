package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const DefaultOptionsPath = "/data/options.json"

// Options is the Home Assistant app configuration. Secrets are deliberately
// kept in this type and are never included in status responses or logs.
type Options struct {
	InfluxURL                string `json:"influx_url"`
	InfluxDatabase           string `json:"influx_database"`
	InfluxUsername           string `json:"influx_username"`
	InfluxPassword           string `json:"influx_password"`
	InfluxVerifyTLS          bool   `json:"influx_verify_tls"`
	RequestTimeoutSeconds    int    `json:"request_timeout_seconds"`
	MaxPoints                int    `json:"max_points"`
	MaxEntitiesPerQuery      int    `json:"max_entities_per_query"`
	MaxSchemaSeries          int    `json:"max_schema_series"`
	MaxLookbackDays          int    `json:"max_lookback_days"`
	OpenAITunnelEnabled      bool   `json:"openai_tunnel_enabled"`
	OpenAITunnelID           string `json:"openai_tunnel_id"`
	OpenAITunnelAPIKey       string `json:"openai_tunnel_api_key"`
	TunnelReadyTimeoutSecond int    `json:"tunnel_ready_timeout_seconds"`
	LogLevel                 string `json:"log_level"`
	HTTPListenAddress        string `json:"-"`
}

func Default() Options {
	return Options{
		InfluxURL:                "http://a0d7b954-influxdb:8086",
		InfluxDatabase:           "home_assistant",
		InfluxVerifyTLS:          true,
		RequestTimeoutSeconds:    20,
		MaxPoints:                2000,
		MaxEntitiesPerQuery:      10,
		MaxSchemaSeries:          10000,
		MaxLookbackDays:          3660,
		TunnelReadyTimeoutSecond: 90,
		LogLevel:                 "info",
		HTTPListenAddress:        ":8080",
	}
}

// Load reads Home Assistant's options JSON when it exists, then applies
// environment overrides. Environment-only configuration keeps local tests and
// non-HA development straightforward.
func Load(path string) (Options, error) {
	cfg := Default()
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&cfg); err != nil {
				return Options{}, fmt.Errorf("decode options file %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist):
			// A missing file is expected outside Home Assistant.
		default:
			return Options{}, fmt.Errorf("read options file %s: %w", path, err)
		}
	}

	if err := applyEnvironment(&cfg, os.LookupEnv); err != nil {
		return Options{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Options{}, err
	}
	return cfg, nil
}

type lookupEnv func(string) (string, bool)

func applyEnvironment(cfg *Options, lookup lookupEnv) error {
	stringValues := []struct {
		name   string
		target *string
	}{
		{"INFLUX_URL", &cfg.InfluxURL},
		{"INFLUX_DATABASE", &cfg.InfluxDatabase},
		{"INFLUX_USERNAME", &cfg.InfluxUsername},
		{"INFLUX_PASSWORD", &cfg.InfluxPassword},
		{"OPENAI_TUNNEL_ID", &cfg.OpenAITunnelID},
		{"OPENAI_TUNNEL_API_KEY", &cfg.OpenAITunnelAPIKey},
		{"LOG_LEVEL", &cfg.LogLevel},
		{"HTTP_LISTEN_ADDRESS", &cfg.HTTPListenAddress},
	}
	for _, item := range stringValues {
		if value, ok := lookup(item.name); ok {
			*item.target = strings.TrimSpace(value)
		}
	}

	boolValues := []struct {
		name   string
		target *bool
	}{
		{"INFLUX_VERIFY_TLS", &cfg.InfluxVerifyTLS},
		{"OPENAI_TUNNEL_ENABLED", &cfg.OpenAITunnelEnabled},
	}
	for _, item := range boolValues {
		if value, ok := lookup(item.name); ok {
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				return fmt.Errorf("parse %s: %w", item.name, err)
			}
			*item.target = parsed
		}
	}

	intValues := []struct {
		name   string
		target *int
	}{
		{"REQUEST_TIMEOUT_SECONDS", &cfg.RequestTimeoutSeconds},
		{"MAX_POINTS", &cfg.MaxPoints},
		{"MAX_ENTITIES_PER_QUERY", &cfg.MaxEntitiesPerQuery},
		{"MAX_SCHEMA_SERIES", &cfg.MaxSchemaSeries},
		{"MAX_LOOKBACK_DAYS", &cfg.MaxLookbackDays},
		{"TUNNEL_READY_TIMEOUT_SECONDS", &cfg.TunnelReadyTimeoutSecond},
	}
	for _, item := range intValues {
		if value, ok := lookup(item.name); ok {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return fmt.Errorf("parse %s: %w", item.name, err)
			}
			*item.target = parsed
		}
	}
	return nil
}

func (o Options) Validate() error {
	parsedURL, err := url.Parse(o.InfluxURL)
	if err != nil {
		return fmt.Errorf("invalid influx_url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return errors.New("influx_url must use http or https")
	}
	if parsedURL.Host == "" {
		return errors.New("influx_url must include a host")
	}
	if parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return errors.New("influx_url must not contain credentials, a query, or a fragment")
	}
	if strings.TrimSpace(o.InfluxDatabase) == "" {
		return errors.New("influx_database is required")
	}
	if o.RequestTimeoutSeconds < 1 || o.RequestTimeoutSeconds > 120 {
		return errors.New("request_timeout_seconds must be between 1 and 120")
	}
	if o.MaxPoints < 1 || o.MaxPoints > 10000 {
		return errors.New("max_points must be between 1 and 10000")
	}
	if o.MaxEntitiesPerQuery < 1 || o.MaxEntitiesPerQuery > 50 {
		return errors.New("max_entities_per_query must be between 1 and 50")
	}
	if o.MaxSchemaSeries < 100 || o.MaxSchemaSeries > 100000 {
		return errors.New("max_schema_series must be between 100 and 100000")
	}
	if o.MaxLookbackDays < 1 || o.MaxLookbackDays > 36500 {
		return errors.New("max_lookback_days must be between 1 and 36500")
	}
	if o.TunnelReadyTimeoutSecond < 5 || o.TunnelReadyTimeoutSecond > 600 {
		return errors.New("tunnel_ready_timeout_seconds must be between 5 and 600")
	}
	if o.HTTPListenAddress == "" {
		return errors.New("HTTP_LISTEN_ADDRESS must not be empty")
	}
	switch strings.ToLower(o.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("log_level must be debug, info, warn, or error")
	}
	if o.OpenAITunnelEnabled {
		if strings.TrimSpace(o.OpenAITunnelID) == "" {
			return errors.New("openai_tunnel_id is required when openai_tunnel_enabled is true")
		}
		if strings.TrimSpace(o.OpenAITunnelAPIKey) == "" {
			return errors.New("openai_tunnel_api_key is required when openai_tunnel_enabled is true")
		}
	}
	return nil
}

func (o Options) RequestTimeout() time.Duration {
	return time.Duration(o.RequestTimeoutSeconds) * time.Second
}

func (o Options) TunnelReadyTimeout() time.Duration {
	return time.Duration(o.TunnelReadyTimeoutSecond) * time.Second
}

func (o Options) MaxLookback() time.Duration {
	return time.Duration(o.MaxLookbackDays) * 24 * time.Hour
}
