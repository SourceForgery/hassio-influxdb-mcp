package influx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 32 << 20

type Series struct {
	Name    string            `json:"name"`
	Tags    map[string]string `json:"tags,omitempty"`
	Columns []string          `json:"columns"`
	Values  [][]any           `json:"values"`
}

type Client struct {
	endpoint string
	database string
	username string
	password string
	http     *http.Client
}

func NewClient(baseURL, database, username, password string, verifyTLS bool, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse InfluxDB URL: %w", err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/query"
	parsed.RawQuery = ""
	parsed.Fragment = ""

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// This is an explicit Home Assistant option for installations using a
		// private, self-signed InfluxDB endpoint.
		InsecureSkipVerify: !verifyTLS, //nolint:gosec
	}
	return &Client{
		endpoint: parsed.String(),
		database: database,
		username: username,
		password: password,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}, nil
}

type responseEnvelope struct {
	Error   string           `json:"error"`
	Results []responseResult `json:"results"`
}

type responseResult struct {
	Error  string   `json:"error"`
	Series []Series `json:"series"`
}

func (c *Client) Query(ctx context.Context, query string) ([]Series, error) {
	form := url.Values{
		"db": {c.database},
		"q":  {query},
	}
	if c.username != "" {
		// InfluxDB 1.x accepts u/p form values. Basic authentication is also
		// set below for compatible proxies and later InfluxDB versions.
		form.Set("u", c.username)
		form.Set("p", c.password)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create InfluxDB request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ha-influxdb-mcp/1")
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query InfluxDB: %w", err)
	}
	defer resp.Body.Close()

	body := io.LimitReader(resp.Body, maxResponseBytes+1)
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read InfluxDB response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("InfluxDB response exceeded 32 MiB limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		if len(message) > 500 {
			message = message[:500] + "..."
		}
		return nil, fmt.Errorf("InfluxDB returned HTTP %d: %s", resp.StatusCode, message)
	}

	var envelope responseEnvelope
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode InfluxDB response: %w", err)
	}
	if envelope.Error != "" {
		return nil, fmt.Errorf("InfluxDB query error: %s", envelope.Error)
	}
	var series []Series
	for _, result := range envelope.Results {
		if result.Error != "" {
			return nil, fmt.Errorf("InfluxDB statement error: %s", result.Error)
		}
		series = append(series, result.Series...)
	}
	return series, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Query(ctx, "SHOW MEASUREMENTS LIMIT 1")
	return err
}
