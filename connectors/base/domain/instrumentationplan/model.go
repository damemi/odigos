// Package instrumentationplan renders a computed instrumentation plan into the agents-api
// InstrumentationInstructions model: text a client displays, with this workload's values already
// substituted.
//
// It is the plan's counterpart to workloadreport — the pure model lives in base/domain/plan and knows nothing
// about the wire, and this is the one place that maps it — but the mapping is deliberately lossy in one
// direction. The plan's fragments (env merge directions, volumes, helper containers, layer attachments) do
// NOT reach the wire. They are how the connector decides what to write and what to judge a workload against;
// putting them on the wire would make any client that acted on them a second implementation of this
// rendering, free to disagree with the one the connector then reports drift against, and would cost an
// agents-api version for every injection mechanic a provider grows.
//
// What crosses the boundary is what a client can actually use: documents to show, notes to show, and the
// blanks that are still the customer's to fill.
package instrumentationplan

import (
	"fmt"
	"strings"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
)

// VariantManual is the delivery variant for a rendering that is prose rather than a tool's syntax. It is what
// the base falls back to when a provider declares no artifacts for a type.
const VariantManual = "manual"

// formatText is the syntax hint for a document with no syntax.
const formatText = "text"

// ToModel converts a computed plan to the instructions served for it. now is passed in rather than read so
// the output is deterministic in tests.
func ToModel(res api.InstrumentationPlanResult, now time.Time) *model.InstrumentationInstructions {
	values := res.Resolved
	return &model.InstrumentationInstructions{
		ServiceName: res.ServiceName,
		Kind:        res.Kind,
		Region:      strPtrOrNil(res.Region),
		Mode:        res.Mode,
		// Notes are substituted too. A note that names the destination endpoint or the reported service name
		// should say the value, not the token — a customer reading "set it to <ODIGOS_SOURCE_NAME>" in the
		// caveats after the snippet already resolved it would reasonably conclude the two disagree.
		Notes:     nonNilStrings(plan.SubstituteAll(instructionNotes(res), values)),
		Documents: documentsToModel(res, values),
		UpdatedAt: now.UTC().Format(time.RFC3339),
	}
}

// instructionNotes is the "Before you deploy" copy. Spec-authored prerequisites on the served artifacts
// win when the first artifact declares them — that is the language's Terraform (or only) document, and
// scraping a NOTES heading out of Template is deliberately not done. When the first artifact has none,
// Plan.Notes is the fallback (kept for providers that have not authored prerequisites yet).
func instructionNotes(res api.InstrumentationPlanResult) []string {
	if len(res.Artifacts) > 0 && len(res.Artifacts[0].Prerequisites) > 0 {
		return res.Artifacts[0].Prerequisites
	}
	return res.Plan.Notes
}

// documentsToModel renders every delivery variant the provider declared for this type, or the base's own
// prose rendering when it declared none.
//
// The list is never empty by construction. A type with no artifacts is a provider that has not authored
// Terraform/CloudFormation/CLI snippets for it yet — which is a gap in convenience, not in knowledge: the
// desired state is fully known, and a customer on an unsupported toolchain needs the change described far
// more than they need it in their own syntax.
func documentsToModel(res api.InstrumentationPlanResult, values map[string]string) []*model.InstructionDocument {
	if len(res.Artifacts) == 0 {
		return []*model.InstructionDocument{proseDocument(res.Plan, values)}
	}

	out := make([]*model.InstructionDocument, 0, len(res.Artifacts))
	for _, a := range res.Artifacts {
		// The template is per TYPE and the values are per WORKLOAD; this is the join, and it is the whole
		// reason these are served through the connector rather than published as a static document.
		content := plan.Substitute(a.Template, values)
		out = append(out, &model.InstructionDocument{
			ID:           a.ID,
			Variant:      orDefault(a.Variant, VariantManual),
			VariantLabel: orDefault(a.VariantLabel, orDefault(a.Variant, VariantManual)),
			Title:        plan.Substitute(a.Title, values),
			Description:  plan.Substitute(a.Description, values),
			Steps:        nonNilStrings(plan.SubstituteAll(a.Steps, values)),
			Format:       orDefault(a.Format, formatText),
			Content:      content,
			// From the CONTENT, not from the steps. A step legitimately says "replace every <ANGLE_BRACKETED>
			// value", which is prose about the convention rather than a blank — reporting it as one would put
			// a fill-in field on screen for a token that appears in no snippet.
			Unresolved: inputsToModel(plan.Unresolved(content, res.Plan.Placeholders)),
		})
	}
	return out
}

// proseDocument is the base's own rendering of the desired state, used when a provider declares no per-tool
// artifacts for a type.
//
// Prose is the one rendering the base can always produce: HCL, CloudFormation YAML, ARM and three CLIs share
// no syntax, so per-tool snippets are hand-authored per provider, but "add this container, mount it here, set
// these variables" is identical across all of them.
func proseDocument(p plan.Plan, values map[string]string) *model.InstructionDocument {
	content := plan.Substitute(plan.Prose(p), values)
	return &model.InstructionDocument{
		ID:           p.Type + "-instrumentation-manual",
		Variant:      VariantManual,
		VariantLabel: "Reference",
		Title:        "What to change",
		Description:  "The change Odigos needs, described step by step. Apply it in whatever tool owns this workload's definition.",
		Steps:        []string{},
		Format:       formatText,
		Content:      content,
		Unresolved:   inputsToModel(plan.Unresolved(content, p.Placeholders)),
	}
}

// inputsToModel maps the unfilled blanks to the wire. nil for none, so the nullable list reads as "nothing
// left to supply" rather than as an empty section a client renders a header for.
func inputsToModel(phs []plan.Placeholder) []*model.InstructionInput {
	if len(phs) == 0 {
		return nil
	}
	out := make([]*model.InstructionInput, 0, len(phs))
	for _, ph := range phs {
		out = append(out, &model.InstructionInput{Placeholder: ph.Token, Description: ph.Description})
	}
	return out
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nonNilStrings normalizes a nil slice to an empty one so a non-null GraphQL list never resolves to null.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// Describe renders the served instructions as a one-line summary for logs.
func Describe(res api.InstrumentationPlanResult) string {
	return fmt.Sprintf("%s (%s/%s) type=%s mode=%s documents=%d resolved=%d",
		res.ServiceName, res.Kind, res.Region, res.InstrumentationType, res.Mode, len(res.Artifacts), len(res.Resolved))
}
