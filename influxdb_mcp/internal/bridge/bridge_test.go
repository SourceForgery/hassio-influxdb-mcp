package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ha-influxdb-mcp/internal/influx"
)

type queryReply struct {
	series []influx.Series
	err    error
}

type stubQueryer struct {
	queries []string
	replies []queryReply
}

func (s *stubQueryer) Query(_ context.Context, query string) ([]influx.Series, error) {
	s.queries = append(s.queries, query)
	if len(s.replies) == 0 {
		return nil, nil
	}
	reply := s.replies[0]
	s.replies = s.replies[1:]
	return reply.series, reply.err
}

func testBridge(queryer Queryer) *Bridge {
	b := New(queryer, Limits{
		MaxPoints:           100,
		MaxEntitiesPerQuery: 5,
		MaxSchemaSeries:     1000,
		MaxLookback:         365 * 24 * time.Hour,
	})
	b.now = func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }
	return b
}

func TestQueryEntityBuildsBoundedAggregateQuery(t *testing.T) {
	t.Parallel()
	stub := &stubQueryer{replies: []queryReply{{series: []influx.Series{{
		Name:    "°C",
		Columns: []string{"time", "value"},
		Values:  [][]any{{"2026-09-18T11:00:00Z", json.Number("12.25")}},
	}}}}}
	b := testBridge(stub)
	out, err := b.QueryEntity(context.Background(), QueryEntityInput{
		EntityID:  "sensor.outdoor_temperature",
		Start:     "-24h",
		Aggregate: "mean",
		Interval:  "1h",
	})
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	want := `SELECT MEAN("value") AS "value" FROM /.*/ WHERE "domain" = 'sensor' AND "entity_id" = 'outdoor_temperature' AND time >= '2026-09-17T12:00:00Z' AND time < '2026-09-18T12:00:00Z' GROUP BY time(1h) fill(null) ORDER BY time ASC LIMIT 100`
	if len(stub.queries) != 1 || stub.queries[0] != want {
		t.Fatalf("query:\n%s\nwant:\n%s", strings.Join(stub.queries, "\n"), want)
	}
	if out.Points != 1 || out.Series[0].Points[0].Value != 12.25 {
		t.Fatalf("output = %#v", out)
	}
}

func TestQueryEntityFallsBackToEntityMeasurement(t *testing.T) {
	t.Parallel()
	stub := &stubQueryer{replies: []queryReply{
		{},
		{series: []influx.Series{{
			Name:    "binary_sensor.front_door",
			Columns: []string{"time", "value"},
			Values:  [][]any{{"2026-09-18T11:55:00Z", "on"}},
		}}},
	}}
	out, err := testBridge(stub).QueryEntity(context.Background(), QueryEntityInput{
		EntityID: "binary_sensor.front_door",
		Start:    "-1h",
	})
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	if len(stub.queries) != 2 {
		t.Fatalf("got %d queries, want fallback query", len(stub.queries))
	}
	if strings.Contains(stub.queries[1], `"domain"`) || !strings.Contains(stub.queries[1], `FROM "binary_sensor.front_door"`) {
		t.Fatalf("unexpected fallback query: %s", stub.queries[1])
	}
	if out.Points != 1 || out.Series[0].Points[0].Value != "on" {
		t.Fatalf("output = %#v", out)
	}
}

func TestQueryValidationBlocksUnboundedAndInjectedInputs(t *testing.T) {
	t.Parallel()
	b := testBridge(&stubQueryer{})
	tests := []QueryEntityInput{
		{EntityID: `sensor.ok' OR true`, Start: "-1h"},
		{EntityID: "sensor.ok", Start: ""},
		{EntityID: "sensor.ok", Start: "-1h", Aggregate: "drop"},
		{EntityID: "sensor.ok", Start: "-1h", Interval: "1h"},
		{EntityID: "sensor.ok", Start: "-1h", Field: "value\nDROP"},
		{EntityID: "sensor.ok", Start: "-1h", Limit: 101},
	}
	for _, input := range tests {
		if _, err := b.QueryEntity(context.Background(), input); err == nil {
			t.Errorf("QueryEntity accepted %#v", input)
		}
	}
}

func TestQuoteIdentifierEscapesQuotesAndBackslashes(t *testing.T) {
	t.Parallel()
	got, err := quoteIdentifier(`a"b\c`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"a\"b\\c"`; got != want {
		t.Fatalf("quoteIdentifier = %q, want %q", got, want)
	}
}

func TestListEntitiesParsesSeriesKeys(t *testing.T) {
	t.Parallel()
	stub := &stubQueryer{replies: []queryReply{{series: []influx.Series{{
		Columns: []string{"key"},
		Values: [][]any{
			{`°C,domain=sensor,entity_id=outdoor_temperature`},
			{`custom\,measurement,domain=sensor,entity_id=garage_temperature`},
			{`binary_sensor.front_door`},
		},
	}}}}}
	out, err := testBridge(stub).ListEntities(context.Background(), ListEntitiesInput{Search: "temperature", Limit: 10})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if out.Matched != 2 {
		t.Fatalf("output = %#v", out)
	}
	if out.Entities[0].EntityID != "sensor.garage_temperature" || out.Entities[0].Measurement != "custom,measurement" {
		t.Fatalf("first entity = %#v", out.Entities[0])
	}
	if out.Entities[1].EntityID != "sensor.outdoor_temperature" {
		t.Fatalf("second entity = %#v", out.Entities[1])
	}
}

func TestListMeasurementsFiltersSortsAndCaps(t *testing.T) {
	t.Parallel()
	stub := &stubQueryer{replies: []queryReply{{series: []influx.Series{{
		Columns: []string{"name"},
		Values:  [][]any{{"kWh"}, {"°C"}, {"°C"}, {"W"}},
	}}}}}
	out, err := testBridge(stub).ListMeasurements(context.Background(), ListMeasurementsInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Matched != 3 || !out.Truncated || len(out.Measurements) != 2 {
		t.Fatalf("output = %#v", out)
	}
}

func TestListFieldsUsesQuotedMeasurement(t *testing.T) {
	t.Parallel()
	stub := &stubQueryer{replies: []queryReply{{series: []influx.Series{{
		Name:    "climate.kitchen",
		Columns: []string{"fieldKey", "fieldType"},
		Values:  [][]any{{"temperature", "float"}, {"state", "string"}},
	}}}}}
	out, err := testBridge(stub).ListFields(context.Background(), ListFieldsInput{Measurement: "climate.kitchen"})
	if err != nil {
		t.Fatal(err)
	}
	if stub.queries[0] != `SHOW FIELD KEYS FROM "climate.kitchen"` {
		t.Fatalf("query = %s", stub.queries[0])
	}
	if out.Matched != 2 {
		t.Fatalf("output = %#v", out)
	}
}

func TestGetLatestSelectsNewestMeasurement(t *testing.T) {
	t.Parallel()
	stub := &stubQueryer{replies: []queryReply{{series: []influx.Series{
		{Name: "°C", Columns: []string{"time", "value"}, Values: [][]any{{"2026-09-18T11:00:00Z", 10.0}}},
		{Name: "K", Columns: []string{"time", "value"}, Values: [][]any{{"2026-09-18T11:30:00Z", 283.5}}},
	}}}}
	out, err := testBridge(stub).GetLatest(context.Background(), GetLatestInput{EntityID: "sensor.outdoor_temperature"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Found || out.Measurement != "K" || out.Value != 283.5 {
		t.Fatalf("output = %#v", out)
	}
}

func TestQueryEntitiesHonorsConfiguredMaximum(t *testing.T) {
	t.Parallel()
	_, err := testBridge(&stubQueryer{}).QueryEntities(context.Background(), QueryEntitiesInput{
		EntityIDs: []string{"sensor.a", "sensor.b", "sensor.c", "sensor.d", "sensor.e", "sensor.f"},
		Start:     "-1h",
	})
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("QueryEntities error = %v", err)
	}
}
