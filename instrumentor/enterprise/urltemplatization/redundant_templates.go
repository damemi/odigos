package urltemplatization

import (
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/actions"
	"github.com/odigos-io/odigos/common/urltemplate"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

type parsedTemplate struct {
	raw      string
	segments []urltemplate.RulePathSegment
}

func knownTemplateStrings(existing []actions.UrlTemplatizationDocumentedTemplate, add []learning.LearnedRule) map[string]any {
	known := make(map[string]any, len(existing)+len(add))
	for _, documented := range existing {
		known[documented.Template] = nil
	}
	for _, rule := range add {
		known[rule.Template] = nil
	}
	return known
}

func parseTemplateStrings(templates map[string]any) []parsedTemplate {
	out := make([]parsedTemplate, 0, len(templates))
	for raw := range templates {
		if raw == "" {
			continue
		}
		rule, err := urltemplate.ParseUserInputRuleString(raw, false)
		if err != nil || len(rule.Segments) == 0 {
			continue
		}
		out = append(out, parsedTemplate{raw: raw, segments: rule.Segments})
	}
	return out
}

// redundantTemplates returns the templates that another template of the same
// segment count already matches. A template is redundant when every segment
// equals the other, or the other has "*" where it has a static value, at least once.
// "*" keeps the path segment as is, so dropping a redundant template does not
// change how any path is templatized, regardless of rule order.
//
// Redundant:
//   - /users/admin given /users/*
//   - /api/v1/namespaces/default/pods/{id} given /api/v1/namespaces/*/pods/{id}
//   - /apis/discovery.k8s.io/v1/endpointslices given /apis/*/v1/*
//
// Not redundant:
//   - /users/admin given /users/{id}: "{id}" rewrites the segment, "*" does not.
//   - /users/{id} given /users/*: "*" would keep the raw id instead of "{id}".
//   - /users/admin given /users/*/orders: different segment count.
//   - /foo/*/bar given /foo/baz/*: neither matches every path of the other.
func redundantTemplates(templates map[string]any) map[string]any {
	byLen := make(map[int][]parsedTemplate)
	for _, tmpl := range parseTemplateStrings(templates) {
		segmentsCount := len(tmpl.segments)
		byLen[segmentsCount] = append(byLen[segmentsCount], tmpl)
	}
	redundant := make(map[string]any)
	for _, sameSegmentsLenRules := range byLen {
		for _, specific := range sameSegmentsLenRules {
			if isRedundant(specific, sameSegmentsLenRules) {
				redundant[specific.raw] = nil
			}
		}
	}
	return redundant
}

func isRedundant(specific parsedTemplate, candidates []parsedTemplate) bool {
	for _, general := range candidates {
		if isMadeRedundantBy(general.segments, specific.segments) {
			return true
		}
	}
	return false
}

// isMadeRedundantBy reports whether specific is redundant given general
// (see redundantTemplates). It is false when both are the same template, since
// at least one "*" in general must replace a static value in specific, so
// callers can compare a template against a list that includes itself.
func isMadeRedundantBy(general, specific []urltemplate.RulePathSegment) bool {
	if len(general) != len(specific) {
		return false
	}
	stricter := false
	for i := range general {
		switch {
		case general[i] == specific[i]:
		case general[i].Wildcard && isStaticSegment(specific[i]):
			stricter = true
		default:
			return false
		}
	}
	return stricter
}

func isStaticSegment(seg urltemplate.RulePathSegment) bool {
	return !seg.Wildcard && seg.TemplateName == ""
}
