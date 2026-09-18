package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ha-influxdb-mcp/internal/influx"
)

var (
	entityIDPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)
	relativePattern = regexp.MustCompile(`^-(\d+)(ms|s|m|h|d|w)$`)
	intervalPattern = regexp.MustCompile(`^[1-9][0-9]*(ms|s|m|h|d|w)$`)
)

type Queryer interface {
	Query(context.Context, string) ([]influx.Series, error)
}

type Limits struct {
	MaxPoints           int
	MaxEntitiesPerQuery int
	MaxSchemaSeries     int
	MaxLookback         time.Duration
}

type Bridge struct {
	queryer Queryer
	limits  Limits
	now     func() time.Time
}

func New(queryer Queryer, limits Limits) *Bridge {
	return &Bridge{queryer: queryer, limits: limits, now: time.Now}
}

type ListMeasurementsInput struct {
	Search string `json:"search,omitempty" jsonschema:"Optional case-insensitive substring filter"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum results to return (1-500)"`
}

type ListMeasurementsOutput struct {
	Measurements []string `json:"measurements"`
	Matched      int      `json:"matched"`
	Truncated    bool     `json:"truncated"`
}

type ListEntitiesInput struct {
	Search string `json:"search,omitempty" jsonschema:"Optional case-insensitive substring matched against entity ID and measurement"`
	Domain string `json:"domain,omitempty" jsonschema:"Optional Home Assistant domain such as sensor or binary_sensor"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum results to return (1-500)"`
}

type EntityRef struct {
	EntityID    string            `json:"entity_id"`
	Measurement string            `json:"measurement"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type ListEntitiesOutput struct {
	Entities  []EntityRef `json:"entities"`
	Matched   int         `json:"matched"`
	Truncated bool        `json:"truncated"`
	ScanLimit int         `json:"scan_limit"`
}

type ListFieldsInput struct {
	Measurement string `json:"measurement,omitempty" jsonschema:"Optional exact measurement name; obtain it from list_entities or list_measurements"`
	Search      string `json:"search,omitempty" jsonschema:"Optional case-insensitive field-name filter"`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum results to return (1-500)"`
}

type FieldRef struct {
	Measurement string `json:"measurement"`
	Field       string `json:"field"`
	Type        string `json:"type"`
}

type ListFieldsOutput struct {
	Fields    []FieldRef `json:"fields"`
	Matched   int        `json:"matched"`
	Truncated bool       `json:"truncated"`
}

type QueryEntityInput struct {
	EntityID    string `json:"entity_id" jsonschema:"Full Home Assistant entity ID such as sensor.outdoor_temperature"`
	Start       string `json:"start" jsonschema:"Inclusive RFC3339 timestamp or relative lookback such as -24h or -7d"`
	End         string `json:"end,omitempty" jsonschema:"Exclusive RFC3339 timestamp; defaults to now"`
	Measurement string `json:"measurement,omitempty" jsonschema:"Optional exact InfluxDB measurement; omit to discover it across measurements"`
	Field       string `json:"field,omitempty" jsonschema:"InfluxDB field to read; defaults to value"`
	Aggregate   string `json:"aggregate,omitempty" jsonschema:"Aggregation: raw, mean, min, max, median, sum, count, spread, first, or last"`
	Interval    string `json:"interval,omitempty" jsonschema:"Optional InfluxDB time bucket such as 15m or 1h; valid only with an aggregate"`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum points per returned series"`
}

type Point struct {
	Time  string `json:"time"`
	Value any    `json:"value"`
}

type TimeSeries struct {
	Measurement string            `json:"measurement"`
	Tags        map[string]string `json:"tags,omitempty"`
	Points      []Point           `json:"points"`
}

type QueryEntityOutput struct {
	EntityID  string       `json:"entity_id"`
	Field     string       `json:"field"`
	Aggregate string       `json:"aggregate"`
	Interval  string       `json:"interval,omitempty"`
	Start     string       `json:"start"`
	End       string       `json:"end"`
	Series    []TimeSeries `json:"series"`
	Points    int          `json:"point_count"`
}

type QueryEntitiesInput struct {
	EntityIDs []string `json:"entity_ids" jsonschema:"Full Home Assistant entity IDs to query with the same time range"`
	Start     string   `json:"start" jsonschema:"Inclusive RFC3339 timestamp or relative lookback such as -24h or -7d"`
	End       string   `json:"end,omitempty" jsonschema:"Exclusive RFC3339 timestamp; defaults to now"`
	Field     string   `json:"field,omitempty" jsonschema:"InfluxDB field to read; defaults to value"`
	Aggregate string   `json:"aggregate,omitempty" jsonschema:"Aggregation: raw, mean, min, max, median, sum, count, spread, first, or last"`
	Interval  string   `json:"interval,omitempty" jsonschema:"Optional InfluxDB time bucket such as 15m or 1h"`
	Limit     int      `json:"limit,omitempty" jsonschema:"Maximum points per returned series"`
}

type EntityQueryResult struct {
	EntityID string             `json:"entity_id"`
	Result   *QueryEntityOutput `json:"result,omitempty"`
	Error    string             `json:"error,omitempty"`
}

type QueryEntitiesOutput struct {
	Results []EntityQueryResult `json:"results"`
}

type GetLatestInput struct {
	EntityID    string `json:"entity_id" jsonschema:"Full Home Assistant entity ID such as sensor.outdoor_temperature"`
	Measurement string `json:"measurement,omitempty" jsonschema:"Optional exact InfluxDB measurement"`
	Field       string `json:"field,omitempty" jsonschema:"InfluxDB field to read; defaults to value"`
}

type GetLatestOutput struct {
	EntityID    string `json:"entity_id"`
	Measurement string `json:"measurement,omitempty"`
	Field       string `json:"field"`
	Found       bool   `json:"found"`
	Time        string `json:"time,omitempty"`
	Value       any    `json:"value,omitempty"`
}

func (b *Bridge) RegisterTools(server *mcp.Server) {
	readOnly := true
	notOpenWorld := false
	notDestructive := false
	annotations := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    readOnly,
			OpenWorldHint:   &notOpenWorld,
			DestructiveHint: &notDestructive,
			IdempotentHint:  true,
		}
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_measurements",
		Title:       "List InfluxDB measurements",
		Description: "List measurement names in the configured Home Assistant InfluxDB database. Use this to discover units or custom measurement names.",
		Annotations: annotations("List InfluxDB measurements"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListMeasurementsInput) (*mcp.CallToolResult, ListMeasurementsOutput, error) {
		out, err := b.ListMeasurements(ctx, in)
		return textResult(fmt.Sprintf("Found %d matching measurements.", out.Matched)), out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_entities",
		Title:       "List Home Assistant entities",
		Description: "Find Home Assistant entity IDs and the InfluxDB measurements that contain them. Call this before querying when the entity or measurement is uncertain.",
		Annotations: annotations("List Home Assistant entities"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListEntitiesInput) (*mcp.CallToolResult, ListEntitiesOutput, error) {
		out, err := b.ListEntities(ctx, in)
		return textResult(fmt.Sprintf("Found %d matching entity/measurement pairs.", out.Matched)), out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_fields",
		Title:       "List InfluxDB fields",
		Description: "List queryable fields and their types, optionally for one exact measurement. Use this for attributes such as climate temperature when the field is not value.",
		Annotations: annotations("List InfluxDB fields"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListFieldsInput) (*mcp.CallToolResult, ListFieldsOutput, error) {
		out, err := b.ListFields(ctx, in)
		return textResult(fmt.Sprintf("Found %d matching fields.", out.Matched)), out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_latest",
		Title:       "Get latest entity value",
		Description: "Get the newest stored value for one Home Assistant entity. This is read-only and searches all measurements unless an exact measurement is supplied.",
		Annotations: annotations("Get latest entity value"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetLatestInput) (*mcp.CallToolResult, GetLatestOutput, error) {
		out, err := b.GetLatest(ctx, in)
		message := "No stored value was found."
		if out.Found {
			message = fmt.Sprintf("Found the latest %s value at %s.", out.EntityID, out.Time)
		}
		return textResult(message), out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "query_entity",
		Title:       "Query entity history",
		Description: "Query bounded historical data for one Home Assistant entity. Supports raw samples or safe server-side aggregations and optional time buckets; it never accepts arbitrary InfluxQL.",
		Annotations: annotations("Query entity history"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QueryEntityInput) (*mcp.CallToolResult, QueryEntityOutput, error) {
		out, err := b.QueryEntity(ctx, in)
		return textResult(fmt.Sprintf("Returned %d points for %s.", out.Points, out.EntityID)), out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "query_entities",
		Title:       "Compare entity histories",
		Description: "Query several Home Assistant entities over one bounded time range using the same field, aggregate, interval, and point limit.",
		Annotations: annotations("Compare entity histories"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QueryEntitiesInput) (*mcp.CallToolResult, QueryEntitiesOutput, error) {
		out, err := b.QueryEntities(ctx, in)
		return textResult(fmt.Sprintf("Queried %d entities.", len(out.Results))), out, err
	})
}

func textResult(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

func (b *Bridge) ListMeasurements(ctx context.Context, in ListMeasurementsInput) (ListMeasurementsOutput, error) {
	limit, err := resultLimit(in.Limit)
	if err != nil {
		return ListMeasurementsOutput{}, err
	}
	series, err := b.queryer.Query(ctx, fmt.Sprintf("SHOW MEASUREMENTS LIMIT %d", b.limits.MaxSchemaSeries))
	if err != nil {
		return ListMeasurementsOutput{}, err
	}
	search := strings.ToLower(strings.TrimSpace(in.Search))
	var names []string
	for _, item := range series {
		nameIndex := columnIndex(item.Columns, "name")
		if nameIndex < 0 {
			continue
		}
		for _, row := range item.Values {
			if nameIndex >= len(row) {
				continue
			}
			name := valueString(row[nameIndex])
			if search == "" || strings.Contains(strings.ToLower(name), search) {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	names = slices.Compact(names)
	matched := len(names)
	if len(names) > limit {
		names = names[:limit]
	}
	return ListMeasurementsOutput{Measurements: names, Matched: matched, Truncated: matched > len(names)}, nil
}

func (b *Bridge) ListEntities(ctx context.Context, in ListEntitiesInput) (ListEntitiesOutput, error) {
	limit, err := resultLimit(in.Limit)
	if err != nil {
		return ListEntitiesOutput{}, err
	}
	domainFilter := strings.ToLower(strings.TrimSpace(in.Domain))
	if domainFilter != "" && !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(domainFilter) {
		return ListEntitiesOutput{}, errors.New("domain contains invalid characters")
	}
	series, err := b.queryer.Query(ctx, fmt.Sprintf("SHOW SERIES LIMIT %d", b.limits.MaxSchemaSeries))
	if err != nil {
		return ListEntitiesOutput{}, err
	}
	search := strings.ToLower(strings.TrimSpace(in.Search))
	seen := make(map[string]bool)
	var entities []EntityRef
	for _, item := range series {
		keyIndex := columnIndex(item.Columns, "key")
		if keyIndex < 0 {
			continue
		}
		for _, row := range item.Values {
			if keyIndex >= len(row) {
				continue
			}
			measurement, tags, err := parseSeriesKey(valueString(row[keyIndex]))
			if err != nil {
				continue
			}
			domain := tags["domain"]
			objectID := tags["entity_id"]
			entityID := ""
			switch {
			case domain != "" && objectID != "":
				entityID = domain + "." + objectID
			case entityIDPattern.MatchString(measurement):
				entityID = measurement
			case entityIDPattern.MatchString(objectID):
				entityID = objectID
			default:
				continue
			}
			if domainFilter != "" && !strings.HasPrefix(entityID, domainFilter+".") {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(entityID), search) && !strings.Contains(strings.ToLower(measurement), search) {
				continue
			}
			key := entityID + "\x00" + measurement
			if seen[key] {
				continue
			}
			seen[key] = true
			entities = append(entities, EntityRef{EntityID: entityID, Measurement: measurement, Tags: tags})
		}
	}
	sort.Slice(entities, func(i, j int) bool {
		if entities[i].EntityID == entities[j].EntityID {
			return entities[i].Measurement < entities[j].Measurement
		}
		return entities[i].EntityID < entities[j].EntityID
	})
	matched := len(entities)
	if len(entities) > limit {
		entities = entities[:limit]
	}
	return ListEntitiesOutput{Entities: entities, Matched: matched, Truncated: matched > len(entities), ScanLimit: b.limits.MaxSchemaSeries}, nil
}

func (b *Bridge) ListFields(ctx context.Context, in ListFieldsInput) (ListFieldsOutput, error) {
	limit, err := resultLimit(in.Limit)
	if err != nil {
		return ListFieldsOutput{}, err
	}
	query := "SHOW FIELD KEYS"
	if strings.TrimSpace(in.Measurement) != "" {
		measurement, err := quoteIdentifier(in.Measurement)
		if err != nil {
			return ListFieldsOutput{}, fmt.Errorf("measurement: %w", err)
		}
		query += " FROM " + measurement
	}
	series, err := b.queryer.Query(ctx, query)
	if err != nil {
		return ListFieldsOutput{}, err
	}
	search := strings.ToLower(strings.TrimSpace(in.Search))
	var fields []FieldRef
	for _, item := range series {
		keyIndex := columnIndex(item.Columns, "fieldKey")
		typeIndex := columnIndex(item.Columns, "fieldType")
		if keyIndex < 0 || typeIndex < 0 {
			continue
		}
		for _, row := range item.Values {
			if keyIndex >= len(row) || typeIndex >= len(row) {
				continue
			}
			field := valueString(row[keyIndex])
			if search != "" && !strings.Contains(strings.ToLower(field), search) {
				continue
			}
			fields = append(fields, FieldRef{Measurement: item.Name, Field: field, Type: valueString(row[typeIndex])})
		}
	}
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].Measurement == fields[j].Measurement {
			return fields[i].Field < fields[j].Field
		}
		return fields[i].Measurement < fields[j].Measurement
	})
	matched := len(fields)
	if len(fields) > limit {
		fields = fields[:limit]
	}
	return ListFieldsOutput{Fields: fields, Matched: matched, Truncated: matched > len(fields)}, nil
}

func (b *Bridge) QueryEntity(ctx context.Context, in QueryEntityInput) (QueryEntityOutput, error) {
	validated, err := b.validateQuery(in)
	if err != nil {
		return QueryEntityOutput{}, err
	}
	query, err := buildHistoryQuery(validated, true)
	if err != nil {
		return QueryEntityOutput{}, err
	}
	series, err := b.queryer.Query(ctx, query)
	if err != nil {
		return QueryEntityOutput{}, err
	}
	if len(series) == 0 && (validated.Measurement == "" || validated.Measurement == validated.EntityID) {
		fallback := validated
		fallback.Measurement = validated.EntityID
		query, err = buildHistoryQuery(fallback, false)
		if err != nil {
			return QueryEntityOutput{}, err
		}
		series, err = b.queryer.Query(ctx, query)
		if err != nil {
			return QueryEntityOutput{}, err
		}
	}
	converted, count := convertSeries(series)
	return QueryEntityOutput{
		EntityID:  validated.EntityID,
		Field:     validated.Field,
		Aggregate: validated.Aggregate,
		Interval:  validated.Interval,
		Start:     validated.Start,
		End:       validated.End,
		Series:    converted,
		Points:    count,
	}, nil
}

func (b *Bridge) QueryEntities(ctx context.Context, in QueryEntitiesInput) (QueryEntitiesOutput, error) {
	if len(in.EntityIDs) == 0 {
		return QueryEntitiesOutput{}, errors.New("entity_ids must contain at least one entity")
	}
	if len(in.EntityIDs) > b.limits.MaxEntitiesPerQuery {
		return QueryEntitiesOutput{}, fmt.Errorf("entity_ids exceeds configured maximum of %d", b.limits.MaxEntitiesPerQuery)
	}
	seen := make(map[string]bool)
	out := QueryEntitiesOutput{Results: make([]EntityQueryResult, 0, len(in.EntityIDs))}
	for _, entityID := range in.EntityIDs {
		if seen[entityID] {
			continue
		}
		seen[entityID] = true
		result, err := b.QueryEntity(ctx, QueryEntityInput{
			EntityID:  entityID,
			Start:     in.Start,
			End:       in.End,
			Field:     in.Field,
			Aggregate: in.Aggregate,
			Interval:  in.Interval,
			Limit:     in.Limit,
		})
		entry := EntityQueryResult{EntityID: entityID}
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.Result = &result
		}
		out.Results = append(out.Results, entry)
	}
	return out, nil
}

func (b *Bridge) GetLatest(ctx context.Context, in GetLatestInput) (GetLatestOutput, error) {
	if err := validateEntityID(in.EntityID); err != nil {
		return GetLatestOutput{}, err
	}
	field := strings.TrimSpace(in.Field)
	if field == "" {
		field = "value"
	}
	quotedField, err := quoteIdentifier(field)
	if err != nil {
		return GetLatestOutput{}, fmt.Errorf("field: %w", err)
	}
	measurement := strings.TrimSpace(in.Measurement)
	from := `/.*/`
	if measurement != "" {
		from, err = quoteIdentifier(measurement)
		if err != nil {
			return GetLatestOutput{}, fmt.Errorf("measurement: %w", err)
		}
	}
	domain, objectID, _ := strings.Cut(in.EntityID, ".")
	where := fmt.Sprintf(`"domain" = %s AND "entity_id" = %s`, quoteString(domain), quoteString(objectID))
	query := fmt.Sprintf(`SELECT LAST(%s) AS "value" FROM %s WHERE %s`, quotedField, from, where)
	series, err := b.queryer.Query(ctx, query)
	if err != nil {
		return GetLatestOutput{}, err
	}
	if len(series) == 0 && (measurement == "" || measurement == in.EntityID) {
		fallbackMeasurement, err := quoteIdentifier(in.EntityID)
		if err != nil {
			return GetLatestOutput{}, err
		}
		series, err = b.queryer.Query(ctx, fmt.Sprintf(`SELECT LAST(%s) AS "value" FROM %s`, quotedField, fallbackMeasurement))
		if err != nil {
			return GetLatestOutput{}, err
		}
	}
	converted, _ := convertSeries(series)
	out := GetLatestOutput{EntityID: in.EntityID, Field: field}
	var newest time.Time
	for _, item := range converted {
		for _, point := range item.Points {
			stamp, parseErr := time.Parse(time.RFC3339Nano, point.Time)
			if !out.Found || (parseErr == nil && stamp.After(newest)) {
				out.Found = true
				out.Measurement = item.Measurement
				out.Time = point.Time
				out.Value = point.Value
				if parseErr == nil {
					newest = stamp
				}
			}
		}
	}
	return out, nil
}

type validatedQuery struct {
	EntityID    string
	Start       string
	End         string
	Measurement string
	Field       string
	Aggregate   string
	Interval    string
	Limit       int
}

func (b *Bridge) validateQuery(in QueryEntityInput) (validatedQuery, error) {
	if err := validateEntityID(in.EntityID); err != nil {
		return validatedQuery{}, err
	}
	now := b.now().UTC()
	start, err := parseTime(in.Start, now)
	if err != nil {
		return validatedQuery{}, fmt.Errorf("start: %w", err)
	}
	end := now
	if strings.TrimSpace(in.End) != "" && strings.TrimSpace(in.End) != "now" {
		end, err = parseTime(in.End, now)
		if err != nil {
			return validatedQuery{}, fmt.Errorf("end: %w", err)
		}
	}
	if !start.Before(end) {
		return validatedQuery{}, errors.New("start must be before end")
	}
	if end.Sub(start) > b.limits.MaxLookback {
		return validatedQuery{}, fmt.Errorf("time range exceeds configured maximum of %s", b.limits.MaxLookback)
	}
	field := strings.TrimSpace(in.Field)
	if field == "" {
		field = "value"
	}
	if _, err := quoteIdentifier(field); err != nil {
		return validatedQuery{}, fmt.Errorf("field: %w", err)
	}
	measurement := strings.TrimSpace(in.Measurement)
	if measurement != "" {
		if _, err := quoteIdentifier(measurement); err != nil {
			return validatedQuery{}, fmt.Errorf("measurement: %w", err)
		}
	}
	aggregate := strings.ToLower(strings.TrimSpace(in.Aggregate))
	if aggregate == "" {
		aggregate = "raw"
	}
	if !validAggregate(aggregate) {
		return validatedQuery{}, fmt.Errorf("unsupported aggregate %q", aggregate)
	}
	interval := strings.TrimSpace(in.Interval)
	if interval != "" {
		if aggregate == "raw" {
			return validatedQuery{}, errors.New("interval requires an aggregate other than raw")
		}
		if !intervalPattern.MatchString(interval) {
			return validatedQuery{}, errors.New("interval must look like 15m, 1h, or 1d")
		}
	}
	limit := in.Limit
	if limit == 0 {
		limit = b.limits.MaxPoints
	}
	if limit < 1 || limit > b.limits.MaxPoints {
		return validatedQuery{}, fmt.Errorf("limit must be between 1 and configured maximum %d", b.limits.MaxPoints)
	}
	return validatedQuery{
		EntityID:    in.EntityID,
		Start:       start.Format(time.RFC3339Nano),
		End:         end.Format(time.RFC3339Nano),
		Measurement: measurement,
		Field:       field,
		Aggregate:   aggregate,
		Interval:    interval,
		Limit:       limit,
	}, nil
}

func buildHistoryQuery(in validatedQuery, includeEntityTags bool) (string, error) {
	field, err := quoteIdentifier(in.Field)
	if err != nil {
		return "", err
	}
	from := `/.*/`
	if in.Measurement != "" {
		from, err = quoteIdentifier(in.Measurement)
		if err != nil {
			return "", err
		}
	}
	selector := field
	if in.Aggregate != "raw" {
		selector = strings.ToUpper(in.Aggregate) + "(" + field + ")"
	}
	whereParts := []string{
		"time >= " + quoteString(in.Start),
		"time < " + quoteString(in.End),
	}
	if includeEntityTags {
		domain, objectID, _ := strings.Cut(in.EntityID, ".")
		whereParts = append([]string{
			`"domain" = ` + quoteString(domain),
			`"entity_id" = ` + quoteString(objectID),
		}, whereParts...)
	}
	query := fmt.Sprintf(`SELECT %s AS "value" FROM %s WHERE %s`, selector, from, strings.Join(whereParts, " AND "))
	if in.Interval != "" {
		query += " GROUP BY time(" + in.Interval + ") fill(null)"
	}
	query += fmt.Sprintf(" ORDER BY time ASC LIMIT %d", in.Limit)
	return query, nil
}

func convertSeries(items []influx.Series) ([]TimeSeries, int) {
	output := make([]TimeSeries, 0, len(items))
	total := 0
	for _, item := range items {
		timeIndex := columnIndex(item.Columns, "time")
		valueIndex := columnIndex(item.Columns, "value")
		if timeIndex < 0 || valueIndex < 0 {
			continue
		}
		series := TimeSeries{Measurement: item.Name, Tags: item.Tags, Points: make([]Point, 0, len(item.Values))}
		for _, row := range item.Values {
			if timeIndex >= len(row) || valueIndex >= len(row) {
				continue
			}
			series.Points = append(series.Points, Point{Time: valueString(row[timeIndex]), Value: normalizeValue(row[valueIndex])})
		}
		if len(series.Points) > 0 {
			total += len(series.Points)
			output = append(output, series)
		}
	}
	return output, total
}

func normalizeValue(value any) any {
	if number, ok := value.(json.Number); ok {
		if integer, err := number.Int64(); err == nil {
			return integer
		}
		if decimal, err := number.Float64(); err == nil {
			return decimal
		}
	}
	return value
}

func columnIndex(columns []string, wanted string) int {
	for index, column := range columns {
		if column == wanted {
			return index
		}
	}
	return -1
}

func valueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func resultLimit(requested int) (int, error) {
	if requested == 0 {
		return 100, nil
	}
	if requested < 1 || requested > 500 {
		return 0, errors.New("limit must be between 1 and 500")
	}
	return requested, nil
}

func validateEntityID(entityID string) error {
	if !entityIDPattern.MatchString(entityID) {
		return errors.New("entity_id must be a lowercase Home Assistant ID such as sensor.outdoor_temperature")
	}
	return nil
}

func validAggregate(value string) bool {
	switch value {
	case "raw", "mean", "min", "max", "median", "sum", "count", "spread", "first", "last":
		return true
	default:
		return false
	}
}

func parseTime(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("value is required")
	}
	if match := relativePattern.FindStringSubmatch(raw); match != nil {
		amount, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return time.Time{}, errors.New("invalid relative time")
		}
		unit := time.Second
		switch match[2] {
		case "ms":
			unit = time.Millisecond
		case "s":
			unit = time.Second
		case "m":
			unit = time.Minute
		case "h":
			unit = time.Hour
		case "d":
			unit = 24 * time.Hour
		case "w":
			unit = 7 * 24 * time.Hour
		}
		if amount > int64((36500*24*time.Hour)/unit) {
			return time.Time{}, errors.New("relative time is too large")
		}
		return now.Add(-time.Duration(amount) * unit), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, errors.New("must be RFC3339 or a relative value such as -24h or -7d")
	}
	return parsed.UTC(), nil
}

func quoteIdentifier(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("must not be empty")
	}
	if utf8.RuneCountInString(value) > 256 {
		return "", errors.New("must not exceed 256 characters")
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return "", errors.New("must not contain control characters")
		}
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`, nil
}

func quoteString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return `'` + value + `'`
}

func parseSeriesKey(key string) (string, map[string]string, error) {
	parts := splitEscaped(key, ',')
	if len(parts) == 0 || parts[0] == "" {
		return "", nil, errors.New("empty series key")
	}
	measurement := unescapeSeriesPart(parts[0])
	tags := make(map[string]string)
	for _, part := range parts[1:] {
		pair := splitEscapedN(part, '=', 2)
		if len(pair) != 2 {
			return "", nil, fmt.Errorf("invalid series tag %q", part)
		}
		tags[unescapeSeriesPart(pair[0])] = unescapeSeriesPart(pair[1])
	}
	return measurement, tags, nil
}

func splitEscaped(value string, separator rune) []string {
	return splitEscapedN(value, separator, -1)
}

func splitEscapedN(value string, separator rune, maximum int) []string {
	var parts []string
	start := 0
	escaped := false
	for index, char := range value {
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == separator && (maximum < 0 || len(parts) < maximum-1) {
			parts = append(parts, value[start:index])
			start = index + utf8.RuneLen(char)
		}
	}
	parts = append(parts, value[start:])
	return parts
}

func unescapeSeriesPart(value string) string {
	var result strings.Builder
	escaped := false
	for _, char := range value {
		if escaped {
			result.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		result.WriteRune(char)
	}
	if escaped {
		result.WriteRune('\\')
	}
	return result.String()
}
