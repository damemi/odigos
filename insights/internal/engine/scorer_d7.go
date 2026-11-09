package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// d7PayloadSize scores HTTP request and response body sizes against learned
// distributions: learning records both in exponential histograms, and after
// promotion either side larger than its baseline p99 deviates.
//
// JSONB shape:
//
//	{"request":{"buckets":[...]}, "response":{"buckets":[...]}}
type d7PayloadSize struct{}

type d7Data struct {
	Request  histogram.Histogram `json:"request"`
	Response histogram.Histogram `json:"response"`
}

func (d7PayloadSize) Class() repo.DeviationClass { return repo.D7PayloadSize }

func (s d7PayloadSize) Score(t *Trace, current json.RawMessage) (bool, json.RawMessage, error) {
	return s.scoreWithLayout(t, current, histogram.DefaultLayout())
}

// decodeBaseline implements baselineDecoder.
func (d7PayloadSize) decodeBaseline(raw json.RawMessage) decodedBaseline {
	d, _ := decodeD7(raw)
	return d
}

func (s d7PayloadSize) scoreWithLayout(t *Trace, current json.RawMessage, layout histogram.Layout) (bool, json.RawMessage, error) {
	baseline, _ := decodeD7(current)
	return baseline.scoreWithLayout(t, layout)
}

// score implements decodedBaseline; it only reads the histograms
// (Total/Percentile), never Observe, so the shared value is left untouched.
func (baseline d7Data) score(t *Trace) (bool, json.RawMessage, error) {
	return baseline.scoreWithLayout(t, histogram.DefaultLayout())
}

func (baseline d7Data) scoreWithLayout(t *Trace, layout histogram.Layout) (bool, json.RawMessage, error) {
	if baseline.Request.Total() == 0 && baseline.Response.Total() == 0 {
		return false, nil, nil
	}

	reqSize, respSize, ok := httpPayloadSizesFromTrace(t)
	if !ok {
		return false, nil, nil
	}

	ev := map[string]any{}
	deviated := false

	if reqSize >= 0 && baseline.Request.Total() > 0 {
		if p99, ok := baseline.Request.Percentile(0.99, layout); ok && reqSize > p99 {
			deviated = true
			ev["request_body_size"] = reqSize
			ev["request_p99"] = p99
		}
	}
	if respSize >= 0 && baseline.Response.Total() > 0 {
		if p99, ok := baseline.Response.Percentile(0.99, layout); ok && respSize > p99 {
			deviated = true
			ev["response_body_size"] = respSize
			ev["response_p99"] = p99
		}
	}
	if !deviated {
		return false, nil, nil
	}

	evJSON, err := json.Marshal(ev)
	if err != nil {
		return true, nil, err
	}
	return true, evJSON, nil
}

// d7ResponseExfilRatio is how many times over the learned response p99 a
// response body must be to be flagged as bulk extraction - a gross overshoot
// ("dumped the whole table"), not "returned a few extra rows".
const d7ResponseExfilRatio = 10

// EnrichRisk implements RiskEnricher: it flags a D7 deviation where the RESPONSE
// body dwarfs the learned baseline - a classic bulk data-extraction signature
// (mass export / scraping). Only the response side matters; an oversized
// request is already covered by the base signal. cfg is unused.
func (d7PayloadSize) EnrichRisk(evidence json.RawMessage, _ enrichConfig) []risk.Signal {
	if len(evidence) == 0 {
		return nil
	}
	var ev struct {
		RespSize int64 `json:"response_body_size"`
		RespP99  int64 `json:"response_p99"`
	}
	if err := json.Unmarshal(evidence, &ev); err != nil {
		return nil
	}
	if ev.RespP99 <= 0 || ev.RespSize < ev.RespP99*d7ResponseExfilRatio {
		return nil
	}
	return []risk.Signal{enricherSignal("d7_response_exfil", risk.CategoryExfiltration,
		fmt.Sprintf("response body %d bytes is ≥%d× the learned p99 (%d) - possible bulk data extraction",
			ev.RespSize, d7ResponseExfilRatio, ev.RespP99))}
}

func (d7PayloadSize) Merge(t *Trace, current json.RawMessage) (json.RawMessage, error) {
	return d7PayloadSize{}.mergeWithLayout(t, current, histogram.DefaultLayout())
}

type d7Learning struct{ data d7Data }

func (d7PayloadSize) decodeLearning(raw json.RawMessage) learningBaseline {
	d, _ := decodeD7(raw)
	return &d7Learning{data: d}
}

func (l *d7Learning) merge(t *Trace) (bool, bool) {
	reqSize, respSize, ok := httpPayloadSizesFromTrace(t)
	if !ok {
		return false, false
	}
	layout := histogram.DefaultLayout()
	grew := false
	if reqSize >= 0 {
		before := histogram.Histogram{Buckets: slices.Clone(l.data.Request.Buckets)}
		l.data.Request.Observe(reqSize, layout)
		grew = histGrew(before, l.data.Request)
	}
	if respSize >= 0 {
		before := histogram.Histogram{Buckets: slices.Clone(l.data.Response.Buckets)}
		l.data.Response.Observe(respSize, layout)
		grew = histGrew(before, l.data.Response) || grew
	}
	return true, grew
}

func (l *d7Learning) encode() (json.RawMessage, error) { return json.Marshal(l.data) }

func (l *d7Learning) members() int { return 0 }

func (d7PayloadSize) mergeWithLayout(t *Trace, current json.RawMessage, layout histogram.Layout) (json.RawMessage, error) {
	baseline, _ := decodeD7(current)
	reqSize, respSize, ok := httpPayloadSizesFromTrace(t)
	if !ok {
		return current, nil
	}

	if reqSize >= 0 {
		baseline.Request.Observe(reqSize, layout)
	}
	if respSize >= 0 {
		baseline.Response.Observe(respSize, layout)
	}
	return json.Marshal(baseline)
}

// grew implements stabilityComparator: the payload-size baseline has grown only
// if a previously-empty request or response bucket is now populated, not when
// counts climb within already-seen buckets.
func (d7PayloadSize) grew(before, after json.RawMessage) bool {
	b, _ := decodeD7(before)
	a, _ := decodeD7(after)
	return histGrew(b.Request, a.Request) || histGrew(b.Response, a.Response)
}

// histGrew reports whether any bucket went from zero to non-zero between two
// snapshots of the same histogram.
func histGrew(before, after histogram.Histogram) bool {
	for i := range after.Buckets {
		if after.Buckets[i] == 0 {
			continue
		}
		if i >= len(before.Buckets) || before.Buckets[i] == 0 {
			return true
		}
	}
	return false
}

func decodeD7(raw json.RawMessage) (d7Data, error) {
	if len(raw) == 0 {
		return d7Data{
			Request:  histogram.New(),
			Response: histogram.New(),
		}, nil
	}
	var d d7Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return d7Data{
			Request:  histogram.New(),
			Response: histogram.New(),
		}, err
	}
	d.Request.Ensure(histogram.LayoutForStored(d.Request))
	d.Response.Ensure(histogram.LayoutForStored(d.Response))
	return d, nil
}

// httpPayloadSizesFromTrace returns request and response body sizes from the
// trace's HTTP spans, preferring SERVER spans then filling gaps from any span
// (so attrs on INTERNAL roots or children are still picked up). Used by Score
// and Merge.
//
// A missing size counts as 0: many services (or their instrumentation) never
// report body sizes, so treating absence as a real 0 lets D7 learn and promote
// normally instead of leaving an empty histogram stuck in "learning" forever.
// An all-zero baseline never deviates (0 is never over a learned p99), so this
// stays silent for genuinely payload-less transactions.
func httpPayloadSizesFromTrace(t *Trace) (reqSize, respSize int64, ok bool) {
	if t == nil {
		return 0, 0, false
	}
	reqSize, respSize = -1, -1
	for _, s := range t.Spans {
		if s == nil || s.Kind != tracepb.Span_SPAN_KIND_SERVER {
			continue
		}
		absorbPayloadSizes(s, &reqSize, &respSize)
	}
	for _, s := range t.Spans {
		if s == nil {
			continue
		}
		absorbPayloadSizes(s, &reqSize, &respSize)
	}
	if reqSize < 0 {
		reqSize = 0
	}
	if respSize < 0 {
		respSize = 0
	}
	return reqSize, respSize, true
}

func absorbPayloadSizes(s *Span, reqSize, respSize *int64) {
	if r, ok := bodySizeFromSpan(s, requestBodySizeKeys); ok && *reqSize < 0 {
		*reqSize = r
	}
	if r, ok := bodySizeFromSpan(s, responseBodySizeKeys); ok && *respSize < 0 {
		*respSize = r
	}
}

var (
	requestBodySizeKeys = []string{
		semconv.HTTPRequestBodySize,
		semconv.LegacyHTTPRequestHeadersContentLength,
		semconv.LegacyHTTPRequestHeaderContentLength,
		semconv.LegacyHTTPRequestContentLengthUncompressed,
		semconv.LegacyHTTPRequestContentLength,
		semconv.HTTPRequestBody,
	}
	responseBodySizeKeys = []string{
		semconv.HTTPResponseBodySize,
		semconv.LegacyHTTPResponseHeadersContentLength,
		semconv.LegacyHTTPResponseHeaderContentLength,
		semconv.LegacyHTTPResponseContentLengthUncompressed,
		semconv.LegacyHTTPResponseContentLength,
		semconv.HTTPResponseBody,
	}
)

func bodySizeFromSpan(s *Span, keys []string) (int64, bool) {
	for _, key := range keys {
		v := s.Attr(key)
		if v == "" {
			continue
		}
		if key == semconv.HTTPRequestBody || key == semconv.HTTPResponseBody {
			return int64(len(v)), true
		}
		if n, ok := parseBodySizeValue(v); ok {
			return n, true
		}
	}
	return 0, false
}

// parseBodySizeValue parses a scalar or OTLP-style string[] header value.
func parseBodySizeValue(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if strings.HasPrefix(v, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err == nil && len(arr) > 0 {
			v = strings.TrimSpace(arr[0])
		}
	}
	v = strings.Trim(v, "\"'")
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		return n, true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
		return int64(f), true
	}
	return 0, false
}
