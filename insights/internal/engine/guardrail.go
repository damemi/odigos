package engine

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// guardrailViolation is one broken (rule, offending item) with its per-item risk
// score (the rule's catalog weight).
type guardrailViolation struct {
	rule  repo.RuleKey
	item  string
	score int
}

// guardrailViolations checks every ENFORCED rule of a guardrail against the
// trace and returns one entry per offending item. Suggest-mode rules are
// observe-only and never produce a violation. Returns nil when nothing is
// violated (the common case).
//
// Each rule pairs an "observed vs allowlist" extractor (forbiddenX) with its
// catalog entry, so adding a rule type is: a catalog line + one extractor + one
// line here.
//
// identityFingerprint is the stamp of the identity scheme tx.Operation was built
// with; allowed_transactions is only enforced when the stored allowlist carries
// the same stamp.
func guardrailViolations(t *Trace, tx repo.Transaction, guardrail repo.ServiceGuardrail, knownServices map[string]struct{}, identityFingerprint string) []guardrailViolation {
	var out []guardrailViolation
	add := func(ruleKey repo.RuleKey, offending []string) {
		if len(offending) == 0 {
			return
		}
		rule, ok := risk.GuardrailRuleByKey(ruleKey)
		if !ok {
			return
		}
		// Every item of a rule carries the same base weight, so score once.
		score := risk.Assess([]risk.Signal{risk.GuardrailSignal(rule, "")}).Score
		for _, item := range offending {
			out = append(out, guardrailViolation{rule: ruleKey, item: item, score: score})
		}
	}

	if enforced(guardrail, risk.RuleAllowedCallers) {
		add(risk.RuleAllowedCallers, forbiddenCallers(t, guardrail))
	}
	if enforced(guardrail, risk.RuleAllowedCallees) {
		add(risk.RuleAllowedCallees, forbiddenCallees(t, guardrail))
	}
	if enforced(guardrail, risk.RuleAllowedEgress) {
		add(risk.RuleAllowedEgress, forbiddenEgress(t, guardrail, knownServices))
	}
	if enforced(guardrail, risk.RuleAllowedTransactions) {
		add(risk.RuleAllowedTransactions, forbiddenTransactions(tx, guardrail, identityFingerprint))
	}
	return out
}

// enforced reports whether a rule is in enforce mode (suggest = observe-only).
func enforced(guardrail repo.ServiceGuardrail, ruleKey repo.RuleKey) bool {
	return repo.NormalizeRuleMode(guardrail.RuleModes[ruleKey]) == repo.RuleModeEnforce
}

// forbiddenCallers returns the trace's caller if it isn't allowed (inbound). The
// root/external ingress (no in-trace caller) is not policed.
func forbiddenCallers(t *Trace, guardrail repo.ServiceGuardrail) []string {
	caller := t.CallerService
	if caller == "" || containsFold(guardrail.RuleLists[risk.RuleAllowedCallers], caller) {
		return nil
	}
	return []string{caller}
}

// forbiddenCallees returns the distinct direct callee services this service
// reached that aren't allowed (outbound). Direct = a first-hop edge out of this
// service, so a dependency's own downstream calls aren't attributed here.
func forbiddenCallees(t *Trace, guardrail repo.ServiceGuardrail) []string {
	allowed := guardrail.RuleLists[risk.RuleAllowedCallees]
	self := t.Root.Service
	var out []string
	seen := map[string]struct{}{}
	for _, e := range edgesFromTrace(t) {
		callee := e.To.Svc
		if e.From.Svc != self || callee == "" || callee == self {
			continue
		}
		if containsFold(allowed, callee) {
			continue
		}
		if _, dup := seen[callee]; dup {
			continue
		}
		seen[callee] = struct{}{}
		out = append(out, callee)
	}
	return out
}

// forbiddenEgress returns the distinct external destinations this service
// reached directly that aren't allowed. It keeps only this service's own
// external calls (owner == self && external), so a downstream dependency's
// egress isn't attributed here. knownServices is the in-mesh service set for
// this service's namespace: a peer whose host names a service there is treated
// as internal even if flagged external, guarding against a callee whose SERVER
// span didn't nest under the caller in this trace. A destination is host:port;
// an allowlist entry of "host" allows any port, "host:port" pins one.
func forbiddenEgress(t *Trace, guardrail repo.ServiceGuardrail, knownServices map[string]struct{}) []string {
	allowed := guardrail.RuleLists[risk.RuleAllowedEgress]
	self := t.Root.Service
	var out []string
	seen := map[string]struct{}{}
	for _, p := range peersFromTrace(t) {
		if p.Host == "" || p.Owner != self || !p.External || isInMeshHost(p.Host, knownServices) {
			continue
		}
		desc := p.Host
		if p.Port > 0 {
			desc = fmt.Sprintf("%s:%d", p.Host, p.Port)
		}
		if containsFold(allowed, p.Host) || containsFold(allowed, desc) {
			continue
		}
		if _, dup := seen[desc]; dup {
			continue
		}
		seen[desc] = struct{}{}
		out = append(out, desc)
	}
	return out
}

// forbiddenTransactions returns the current transaction identity when it is not
// on the allowed_transactions allowlist. tx.Operation must already include
// identity dimensions (same value processOne persists). An empty operation is
// ignored.
//
// The allowlist holds whole identity strings, so it only means anything for the
// identity scheme it was seeded under. That scheme changes whenever an operator
// edits the transaction identity dimensions (say, starts splitting transactions
// by HTTP status), and every replica picks the edit up on its own reload tick -
// so for a short window one replica builds identities the new way while still
// holding the allowlist seeded the old way. Enforcing then would report every
// relearned transaction as forbidden. Comparing the two stamps, both of which
// this replica already has in memory, rules that out without waiting for any
// reload. Enforcement resumes once a fresh allowlist is written for the new
// scheme (auto-enable after re-promote, or an operator action).
func forbiddenTransactions(tx repo.Transaction, guardrail repo.ServiceGuardrail, identityFingerprint string) []string {
	if tx.Operation == "" {
		return nil
	}
	// An unstamped rule reads as "", which never equals a real fingerprint, so
	// an allowlist of unknown provenance is left unenforced.
	if guardrail.RuleFingerprints[risk.RuleAllowedTransactions] != identityFingerprint {
		return nil
	}
	identity := repo.TransactionIdentity(tx.Kind, tx.Operation)
	if containsExact(guardrail.RuleLists[risk.RuleAllowedTransactions], identity) {
		return nil
	}
	return []string{identity}
}

// containsFold reports whether list contains target case-insensitively, trimming
// surrounding whitespace on each entry (allowlists are hand-edited, so entries
// may carry stray spaces).
func containsFold(list []string, target string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), target) {
			return true
		}
	}
	return false
}

// containsExact reports whether list contains target after trim (case-sensitive).
// Used for transaction identities where path case can matter.
func containsExact(list []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, v := range list {
		if strings.TrimSpace(v) == target {
			return true
		}
	}
	return false
}
