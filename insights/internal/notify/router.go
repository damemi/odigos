package notify

import (
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Router answers "which integrations should receive this finding", resolved
// from the policy that produced it. Anomalies route by the alert policy they
// fired under; violations by the guardrail of their scope. An empty list on
// an override falls back to the global alert policy's list, which is the
// default route for everything.
type Router struct {
	byPolicyID   map[int64][]string
	globalPolicy []string
	guardrails   repo.ScopeIndex[[]string]
}

// NewRouter indexes the current policies and guardrails. Rebuilt every tick
// so an edit is picked up without a restart.
func NewRouter(policies []repo.Policy, guardrails []repo.ServiceGuardrail) *Router {
	r := &Router{byPolicyID: make(map[int64][]string, len(policies))}
	for _, p := range policies {
		r.byPolicyID[p.ID] = p.IntegrationIDs
		if repo.NormalizePolicyScope(p.Scope) == repo.ScopeGlobal {
			r.globalPolicy = p.IntegrationIDs
		}
	}
	for _, g := range guardrails {
		if len(g.IntegrationIDs) == 0 {
			continue
		}
		r.guardrails.Put(g.Scope, g.ScopeKey, g.IntegrationIDs)
	}
	return r
}

// ForAnomaly returns the integration ids routed for an anomaly issue.
func (r *Router) ForAnomaly(a repo.AnomalyIssue) []string {
	if ids := r.byPolicyID[a.PolicyID]; len(ids) > 0 {
		return ids
	}
	return r.globalPolicy
}

// ForViolation returns the integration ids routed for a violation group.
func (r *Router) ForViolation(g repo.GuardrailViolationGroup) []string {
	if ids, ok := r.guardrails.Resolve(0, g.ScopeKey); ok && len(ids) > 0 {
		return ids
	}
	return r.globalPolicy
}

// Routes reports whether integrationID is in ids.
func Routes(ids []string, integrationID string) bool {
	for _, id := range ids {
		if id == integrationID {
			return true
		}
	}
	return false
}
