package apimap

import (
	"encoding/json"
	"fmt"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// HistogramPresentation builds the chart-ready BaselineHistogram for metric
// classes that store exponential histograms. Returns nil when the class is not
// histogram-backed or when data cannot be decoded into buckets. Shared by the
// product API mapper and the debug UI so both render the same bounds/labels.
func HistogramPresentation(class repo.DeviationClass, data json.RawMessage) *apitypes.BaselineHistogram {
	if len(data) == 0 {
		return nil
	}
	switch class {
	case repo.D3Latency:
		var d struct {
			Latency histogram.Histogram `json:"latency"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil
		}
		layout := histogram.LayoutForStored(d.Latency)
		return &apitypes.BaselineHistogram{
			Unit:              apitypes.Us,
			LayoutFingerprint: layout.Fingerprint(),
			Series: []apitypes.BaselineHistogramSeries{
				histogramSeries("latency", "Entry latency", apitypes.Us, d.Latency, layout),
			},
		}
	case repo.D8PayloadSize:
		var d struct {
			Request  histogram.Histogram `json:"request"`
			Response histogram.Histogram `json:"response"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return nil
		}
		reqLayout := histogram.LayoutForStored(d.Request)
		respLayout := histogram.LayoutForStored(d.Response)
		fp := reqLayout.Fingerprint()
		if respLayout.Fingerprint() != fp {
			fp = histogram.MergeLayout(d.Request, d.Response).Fingerprint()
		}
		return &apitypes.BaselineHistogram{
			Unit:              apitypes.Bytes,
			LayoutFingerprint: fp,
			Series: []apitypes.BaselineHistogramSeries{
				histogramSeries("request", "Request body", apitypes.Bytes, d.Request, reqLayout),
				histogramSeries("response", "Response body", apitypes.Bytes, d.Response, respLayout),
			},
		}
	default:
		return nil
	}
}

func histogramSeries(name, label string, unit apitypes.BaselineHistogramUnit, h histogram.Histogram, layout histogram.Layout) apitypes.BaselineHistogramSeries {
	bars := h.Bars(layout)
	out := make([]apitypes.BaselineHistogramBar, len(bars))
	for i, b := range bars {
		out[i] = apitypes.BaselineHistogramBar{
			Lo:    b.Lo,
			Hi:    b.Hi,
			Count: int64(b.Count),
			Label: formatHistogramBarLabel(unit, b.Lo, b.Hi),
		}
	}
	return apitypes.BaselineHistogramSeries{Name: name, Label: label, Bars: out}
}

func formatHistogramBarLabel(unit apitypes.BaselineHistogramUnit, lo, hi int64) string {
	switch unit {
	case apitypes.Us:
		if hi <= lo {
			return formatMicros(lo)
		}
		return formatMicros(lo) + "–" + formatMicros(hi-1)
	case apitypes.Bytes:
		if hi <= lo {
			return formatBytes(lo)
		}
		return formatBytes(lo) + "–" + formatBytes(hi-1)
	default:
		if hi <= lo {
			return fmt.Sprintf("%d", lo)
		}
		return fmt.Sprintf("%d–%d", lo, hi-1)
	}
}

func formatMicros(us int64) string {
	switch {
	case us < 1000:
		return fmt.Sprintf("%dµs", us)
	case us < 1_000_000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	default:
		return fmt.Sprintf("%.2fs", float64(us)/1_000_000)
	}
}

func formatBytes(n int64) string {
	v := uint64(n)
	if n < 0 {
		v = 0
	}
	switch {
	case v < 1024:
		return fmt.Sprintf("%d B", v)
	case v < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(v)/1024)
	case v < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(v)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(v)/(1024*1024*1024))
	}
}
