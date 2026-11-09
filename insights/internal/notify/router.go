package notify

import (
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Router answers "should this finding be sent to this integration" from the
// source that produced it: the alert policy an anomaly fired under, or the
// guardrail of a violation's scope. Delivery is on by default; a source opts
// out with its Delivery settings. A finding whose source is gone (deleted)
// keeps the default and is delivered.
type Router struct {
	policies   map[int64]repo.Delivery
	guardrails map[string]repo.Delivery
}

// NewRouter indexes the current policies and guardrails. Rebuilt every tick
// so an edit is picked up without a restart.
func NewRouter(policies []repo.Policy, guardrails []repo.ServiceGuardrail) *Router {
	r := &Router{
		policies:   make(map[int64]repo.Delivery, len(policies)),
		guardrails: make(map[string]repo.Delivery, len(guardrails)),
	}
	for _, p := range policies {
		r.policies[p.ID] = p.Delivery
	}
	for _, g := range guardrails {
		r.guardrails[g.ScopeKey] = g.Delivery
	}
	return r
}

// Delivers reports whether a finding should be sent to integrationID.
func (r *Router) Delivers(ev Event, integrationID string) bool {
	switch ev.Kind {
	case KindAnomaly:
		return r.policies[ev.PolicyID].Delivers(integrationID)
	case KindViolation:
		return r.guardrails[ev.ScopeKey].Delivers(integrationID)
	}
	return false
}
