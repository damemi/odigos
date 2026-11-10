// Package schema is the versioned JSON contract between the interrogation
// control loop and the configured chat-completions model.
package schema

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odigos-io/odigos/common/api/instrumentationrules"
)

const Version = "1"

const (
	CategoryObservability = "observability"
	CategorySecurity      = "security"
	CategoryPerformance   = "performance"

	LangJava = "java"
	LangGo   = "go"
	LangPHP  = "php"
	LangCpp  = "cpp"
)

const maxPerCategory = 5

// AnalysisResponse is the only accepted model output.
type AnalysisResponse struct {
	SchemaVersion string `json:"schemaVersion" jsonschema:"required,enum=1"`
	TransactionID string `json:"transactionId" jsonschema:"required"`
	// maxItems=15 is the per-category cap (5×3); enforced in Validate as well.
	Recommendations []Recommendation `json:"recommendations" jsonschema:"required,maxItems=15"`
}

// Recommendation is one custom-probe suggestion.
type Recommendation struct {
	Category string `json:"category" jsonschema:"required,enum=observability,enum=security,enum=performance"`
	Function string `json:"function" jsonschema:"required"`
	Language string `json:"language" jsonschema:"required,enum=java,enum=go,enum=php,enum=cpp"`
	Probe    Probe  `json:"probe" jsonschema:"required"`
	// Why must cite evidence (function name and a call-graph edge).
	Why string `json:"why" jsonschema:"required" jsonschema_description:"1-2 sentences citing the exact function name plus a caller→callee edge from the evidence; say what the identifier and graph position imply. No vague domain filler."`
}

// Probe is the language-specific target, matching add_custom_instrumentation.
// All fields are required in model JSON (empty string when unused) for strict json_schema.
type Probe struct {
	ClassName          string `json:"className" jsonschema:"required"`
	MethodName         string `json:"methodName" jsonschema:"required"`
	PackageName        string `json:"packageName" jsonschema:"required"`
	FunctionName       string `json:"functionName" jsonschema:"required"`
	ReceiverName       string `json:"receiverName" jsonschema:"required"`
	ReceiverMethodName string `json:"receiverMethodName" jsonschema:"required"`
	Signature          string `json:"signature" jsonschema:"required"`
}

// Parse decodes and validates a model JSON body.
func Parse(raw []byte) (*AnalysisResponse, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty analysis response")
	}
	var resp AnalysisResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode analysis response: %w", err)
	}
	if err := resp.Validate(); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Validate checks version, categories, caps, and probe shapes.
func (r *AnalysisResponse) Validate() error {
	if r == nil {
		return fmt.Errorf("nil analysis response")
	}
	if r.SchemaVersion != Version {
		return fmt.Errorf("unsupported schemaVersion %q (want %q)", r.SchemaVersion, Version)
	}
	if strings.TrimSpace(r.TransactionID) == "" {
		return fmt.Errorf("transactionId is required")
	}

	counts := map[string]int{}
	for i, rec := range r.Recommendations {
		if err := rec.validate(); err != nil {
			return fmt.Errorf("recommendations[%d]: %w", i, err)
		}
		counts[rec.Category]++
		if counts[rec.Category] > maxPerCategory {
			return fmt.Errorf("more than %d recommendations for category %q", maxPerCategory, rec.Category)
		}
	}
	return nil
}

func (r Recommendation) validate() error {
	switch r.Category {
	case CategoryObservability, CategorySecurity, CategoryPerformance:
	default:
		return fmt.Errorf("unknown category %q", r.Category)
	}
	if strings.TrimSpace(r.Function) == "" {
		return fmt.Errorf("function is required")
	}
	if strings.TrimSpace(r.Why) == "" {
		return fmt.Errorf("why is required")
	}
	ci, err := r.ToCustomInstrumentations()
	if err != nil {
		return err
	}
	if err := ci.Verify(); err != nil {
		return err
	}
	if r.Language == LangCpp {
		if err := ci.Cpp[0].Verify(); err != nil {
			return err
		}
	}
	return nil
}

// ToCustomInstrumentations maps one recommendation onto the rule probe types.
// Why, category, and the time of this call are stored on the probe. Agents do not receive that metadata.
func (r Recommendation) ToCustomInstrumentations() (*instrumentationrules.CustomInstrumentationsWithMetadata, error) {
	return r.ToCustomInstrumentationsAt(time.Now())
}

// ToCustomInstrumentationsAt is ToCustomInstrumentations with a fixed AddedAt,
// so rebuilding a rule from stored recommendations yields the same spec.
func (r Recommendation) ToCustomInstrumentationsAt(addedAt time.Time) (*instrumentationrules.CustomInstrumentationsWithMetadata, error) {
	meta := r.probeMetadata(addedAt)
	ci := &instrumentationrules.CustomInstrumentationsWithMetadata{}
	switch r.Language {
	case LangJava:
		ci.Java = []instrumentationrules.JavaCustomProbeWithMetadata{{
			JavaCustomProbe: instrumentationrules.JavaCustomProbe{
				ClassName:  r.Probe.ClassName,
				MethodName: r.Probe.MethodName,
			},
			CustomProbeMetadata: meta,
		}}
	case LangGo:
		ci.Golang = []instrumentationrules.GolangCustomProbeWithMetadata{{
			GolangCustomProbe: instrumentationrules.GolangCustomProbe{
				PackageName:        r.Probe.PackageName,
				FunctionName:       r.Probe.FunctionName,
				ReceiverName:       r.Probe.ReceiverName,
				ReceiverMethodName: r.Probe.ReceiverMethodName,
			},
			CustomProbeMetadata: meta,
		}}
	case LangPHP:
		ci.Php = []instrumentationrules.PhpCustomProbeWithMetadata{{
			PhpCustomProbe: instrumentationrules.PhpCustomProbe{
				ClassName:    r.Probe.ClassName,
				FunctionName: r.Probe.FunctionName,
			},
			CustomProbeMetadata: meta,
		}}
	case LangCpp:
		ci.Cpp = []instrumentationrules.CppCustomProbeWithMetadata{{
			CppCustomProbe:      instrumentationrules.CppCustomProbe{Signature: r.Probe.Signature},
			CustomProbeMetadata: meta,
		}}
	default:
		return nil, fmt.Errorf("unknown language %q", r.Language)
	}
	return ci, nil
}

func (r Recommendation) probeMetadata(addedAt time.Time) instrumentationrules.CustomProbeMetadata {
	return instrumentationrules.CustomProbeMetadata{
		Notes:    strings.TrimSpace(r.Why),
		Category: instrumentationrules.CustomProbeCategory(r.Category),
		AddedAt:  addedAt.UTC().Format(time.RFC3339),
	}
}

// MergeCustomInstrumentations concatenates validated recommendations by language.
func MergeCustomInstrumentations(recs []Recommendation) (*instrumentationrules.CustomInstrumentationsWithMetadata, error) {
	out := &instrumentationrules.CustomInstrumentationsWithMetadata{}
	for i, rec := range recs {
		ci, err := rec.ToCustomInstrumentations()
		if err != nil {
			return nil, fmt.Errorf("recommendations[%d]: %w", i, err)
		}
		out.Java = append(out.Java, ci.Java...)
		out.Golang = append(out.Golang, ci.Golang...)
		out.Php = append(out.Php, ci.Php...)
		out.Cpp = append(out.Cpp, ci.Cpp...)
	}
	if err := out.Verify(); err != nil {
		return nil, err
	}
	return out, nil
}
