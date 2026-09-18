package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ha-influxdb-mcp/internal/config"
)

func newTestRuntime(t *testing.T, influxHandler http.Handler) (*Runtime, *httptest.Server) {
	t.Helper()
	influxServer := httptest.NewServer(influxHandler)
	t.Cleanup(influxServer.Close)
	cfg := config.Default()
	cfg.InfluxURL = influxServer.URL
	cfg.InfluxDatabase = "home_assistant"
	cfg.InfluxUsername = "reader"
	cfg.InfluxPassword = "not-returned"
	cfg.OpenAITunnelEnabled = false
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, err := New(cfg, "test-version", logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runtime, influxServer
}

func TestStreamableHTTPMCPListsAndCallsTools(t *testing.T) {
	t.Parallel()
	queries := make(chan string, 4)
	runtime, _ := newTestRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		query := r.Form.Get("q")
		queries <- query
		if !strings.HasPrefix(query, "SELECT") {
			t.Errorf("unexpected InfluxQL: %s", query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"series":[{"name":"°C","tags":{"domain":"sensor","entity_id":"outdoor_temperature"},"columns":["time","value"],"values":[["2026-09-18T10:00:00Z",12.5]]}]}]}`))
	}))
	mcpHTTP := httptest.NewServer(runtime.Handler())
	defer mcpHTTP.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: mcpHTTP.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 6 {
		t.Fatalf("tool count = %d, want 6", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", tool.Name)
		}
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "query_entity",
		Arguments: map[string]any{
			"entity_id": "sensor.outdoor_temperature",
			"start":     "2026-09-18T00:00:00Z",
			"end":       "2026-09-18T12:00:00Z",
			"aggregate": "mean",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned tool error: %#v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"point_count":1`) || !strings.Contains(string(encoded), `"value":12.5`) {
		t.Fatalf("structured content = %s", encoded)
	}
	select {
	case query := <-queries:
		if !strings.Contains(query, `MEAN("value")`) || !strings.Contains(query, `"entity_id" = 'outdoor_temperature'`) {
			t.Fatalf("InfluxQL = %s", query)
		}
	case <-time.After(time.Second):
		t.Fatal("fake InfluxDB did not receive a query")
	}
}

func TestHealthReadyAndStatusNeverExposeSecrets(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{}]}`))
	}))
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()

	for _, path := range []string{"/healthz", "/readyz", "/"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d body=%s", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "not-returned") || strings.Contains(string(body), "reader") {
			t.Fatalf("GET %s exposed credentials: %s", path, body)
		}
	}
}
