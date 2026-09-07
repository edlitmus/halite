package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/edlitmus/halite/internal/safehttp"
)

// OTLPExporter posts spans as OTLP over HTTP with JSON encoding, which
// is what SPEC 26.3 names.
//
// # The two things this format does that proto3 JSON does not
//
// OTLP/JSON is proto3's JSON mapping with two documented departures, and
// both are easy to get wrong in a way that produces a body a collector
// accepts and misreads:
//
//   - **Trace and span identifiers are hex, not base64.** Proto3 encodes
//     a `bytes` field as base64; the OTLP specification overrides that
//     for `trace_id`, `span_id` and `parent_span_id` and requires lower
//     case hex. A base64 identifier is still a valid string, so a
//     collector takes it and the trace never joins up.
//   - **64-bit numbers are strings.** Proto3's mapping puts `uint64` and
//     `fixed64` in JSON strings because a JSON number cannot carry them
//     exactly, and a nanosecond timestamp is well past the point where a
//     float64 starts rounding. Emitting them as numbers loses the low
//     digits, which is precisely the resolution a span duration is made
//     of.
//
// Neither is a preference. Both are checked in otlp_test.go against the
// example in the specification's own documentation.
type OTLPExporter struct {
	// Endpoint is the collector's traces URL. OTLP/HTTP puts it at
	// `/v1/traces`, and this appends that when the configured endpoint
	// is a bare base — an operator who writes the base and one who
	// writes the full path both get a working exporter.
	Endpoint string
	// Headers are sent with every request, for a collector that wants
	// an API key.
	Headers map[string]string
	// Client is the HTTP client. Built through safehttp when nil, which
	// applies SPEC 25's outbound rules: a collector address that
	// resolves to a cloud metadata service is refused rather than
	// posted to.
	//
	// A collector on a private address is fine and needs no opt-in.
	// safehttp denies link-local and the metadata ranges — the set that
	// hands out credentials — and deliberately not 10.0.0.0/8, which is
	// where a collector nearly always is.
	Client *http.Client
	// Timeout bounds one export.
	Timeout time.Duration
}

// TracesPath is where OTLP/HTTP puts traces.
const TracesPath = "/v1/traces"

func (e *OTLPExporter) url() string {
	base := e.Endpoint
	if base == "" {
		return ""
	}
	// A configured endpoint that already names the path is used as it
	// is. Appending blindly produces `/v1/traces/v1/traces`, which a
	// collector answers with a 404 that names neither the setting nor
	// the mistake.
	if len(base) >= len(TracesPath) && base[len(base)-len(TracesPath):] == TracesPath {
		return base
	}
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + TracesPath
}

func (e *OTLPExporter) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return safehttp.Client(safehttp.Options{Timeout: timeout})
}

// ExportSpans posts one batch.
func (e *OTLPExporter) ExportSpans(ctx context.Context, service, version string, spans []*Span) error {
	if len(spans) == 0 {
		return nil
	}
	url := e.url()
	if url == "" {
		return fmt.Errorf("no tracing endpoint is configured")
	}
	body, err := json.Marshal(BuildPayload(service, version, spans))
	if err != nil {
		return fmt.Errorf("encoding %d spans: %w", len(spans), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.Headers {
		req.Header.Set(k, v)
	}
	res, err := e.client().Do(req)
	if err != nil {
		return fmt.Errorf("posting %d spans to %s: %w", len(spans), url, err)
	}
	defer res.Body.Close()
	// The body is read and discarded rather than ignored, so the
	// connection can be reused: a collector taking a span every five
	// seconds should not need a new connection for each.
	_, _ = safehttp.Body(res.Body, 1<<20)
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s answered %d for %d spans", url, res.StatusCode, len(spans))
	}
	return nil
}

// The OTLP JSON structures, named as the specification names them.
//
// Written out rather than generated, and kept small: this is the subset
// SPEC 26.3 needs — a resource, one scope, and spans. Events, links and
// the rest of the schema are omitted because nothing here produces them,
// and a struct field that is always empty is one somebody eventually
// fills in wrongly.

type otlpPayload struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpSpan struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	// ParentSpanId is omitted for a root span rather than sent as an
	// empty or zero string: a collector reads a present-but-zero parent
	// as a parent that does not exist, and hangs the span off nothing.
	ParentSpanID string `json:"parentSpanId,omitempty"`
	Name         string `json:"name"`
	Kind         int    `json:"kind"`
	// Times are strings. See the type comment: a nanosecond timestamp
	// does not survive a JSON number.
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Status            *otlpStatus    `json:"status,omitempty"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpKeyValue struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

// otlpValue is OTLP's AnyValue, with the four kinds this build produces.
//
// Exactly one field is set. A value with none is a collector's decision
// to make and every collector makes a different one, so `attrValue`
// never produces one.
type otlpValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

// BuildPayload renders a batch as OTLP/JSON.
//
// Exported so that a test can read what would be sent without a
// collector to send it to, which is the only way to check a wire format
// this build owns rather than imports.
func BuildPayload(service, version string, spans []*Span) any {
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		if s == nil || s.Finish.IsZero() {
			// A span that never ended is one whose duration is unknown,
			// and a collector shown a zero end time draws it as ending
			// in 1970. Dropping it is the honest answer; it can only
			// arrive here by a caller ending the tracer mid-span.
			continue
		}
		span := otlpSpan{
			TraceID:           s.Context.TraceID.String(),
			SpanID:            s.Context.SpanID.String(),
			Name:              s.Name,
			Kind:              int(s.Kind),
			StartTimeUnixNano: strconv.FormatInt(s.Start.UnixNano(), 10),
			EndTimeUnixNano:   strconv.FormatInt(s.Finish.UnixNano(), 10),
			Attributes:        attrsOf(s.Attributes()),
		}
		if !s.Parent.IsZero() {
			span.ParentSpanID = s.Parent.String()
		}
		if s.Status != StatusUnset {
			span.Status = &otlpStatus{Code: int(s.Status), Message: s.Message}
		}
		out = append(out, span)
	}

	return otlpPayload{ResourceSpans: []otlpResourceSpans{{
		Resource: otlpResource{Attributes: attrsOf(map[string]any{
			// The two OpenTelemetry's semantic conventions require of
			// any resource. `service.name` is what a collector groups
			// by, and a batch without it is filed under "unknown".
			"service.name":    service,
			"service.version": version,
		})},
		ScopeSpans: []otlpScopeSpans{{
			Scope: otlpScope{Name: "halite", Version: version},
			Spans: out,
		}},
	}}}
}

// attrsOf renders attributes in a stable order.
//
// Sorted by key, because a payload that differs only in map iteration
// order is one a test cannot compare and a reader cannot diff.
func attrsOf(attrs map[string]any) []otlpKeyValue {
	if len(attrs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]otlpKeyValue, 0, len(keys))
	for _, k := range keys {
		v, ok := attrValue(attrs[k])
		if !ok {
			continue
		}
		out = append(out, otlpKeyValue{Key: k, Value: v})
	}
	return out
}

// attrValue maps a Go value onto OTLP's AnyValue.
//
// Anything this does not have a kind for becomes its printed form
// rather than being dropped: an attribute somebody set is one they
// wanted to see, and a string of it is more use than its absence.
func attrValue(v any) (otlpValue, bool) {
	switch t := v.(type) {
	case nil:
		return otlpValue{}, false
	case string:
		return otlpValue{StringValue: &t}, true
	case bool:
		return otlpValue{BoolValue: &t}, true
	case int:
		s := strconv.FormatInt(int64(t), 10)
		return otlpValue{IntValue: &s}, true
	case int64:
		s := strconv.FormatInt(t, 10)
		return otlpValue{IntValue: &s}, true
	case float64:
		return otlpValue{DoubleValue: &t}, true
	case time.Duration:
		// Milliseconds, because that is what a person reads a duration
		// in and what every other duration this build reports uses.
		ms := float64(t) / float64(time.Millisecond)
		return otlpValue{DoubleValue: &ms}, true
	default:
		s := fmt.Sprint(t)
		return otlpValue{StringValue: &s}, true
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
