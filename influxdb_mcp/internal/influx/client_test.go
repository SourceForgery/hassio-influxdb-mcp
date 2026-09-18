package influx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryUsesV1HTTPAPIAndParsesSeries(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("db"); got != "home_assistant" {
			t.Errorf("db = %q", got)
		}
		if got := r.Form.Get("q"); got != `SELECT "value" FROM "°C"` {
			t.Errorf("q = %q", got)
		}
		if got := r.Form.Get("u"); got != "reader" {
			t.Errorf("u = %q", got)
		}
		if got := r.Form.Get("p"); got != "secret" {
			t.Errorf("p was not supplied")
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "reader" || password != "secret" {
			t.Errorf("BasicAuth = %q/%q/%v", username, password, ok)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"series":[{"name":"°C","tags":{"entity_id":"outside"},"columns":["time","value"],"values":[["2026-09-18T00:00:00Z",12.5]]}]}]}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "home_assistant", "reader", "secret", true, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	series, err := client.Query(context.Background(), `SELECT "value" FROM "°C"`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(series) != 1 || series[0].Name != "°C" {
		t.Fatalf("series = %#v", series)
	}
	value, ok := series[0].Values[0][1].(json.Number)
	if !ok || value.String() != "12.5" {
		t.Fatalf("value = %#v", series[0].Values[0][1])
	}
}

func TestQuerySurfacesInfluxStatementError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"error":"database not found: nope"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "nope", "", "", true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Query(context.Background(), "SHOW MEASUREMENTS")
	if err == nil || !strings.Contains(err.Error(), "database not found") {
		t.Fatalf("Query error = %v", err)
	}
}

func TestQueryRejectsOversizedResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "ha", "", "", true, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Query(context.Background(), "SHOW MEASUREMENTS")
	if err == nil || !strings.Contains(err.Error(), "32 MiB") {
		t.Fatalf("Query error = %v", err)
	}
}
