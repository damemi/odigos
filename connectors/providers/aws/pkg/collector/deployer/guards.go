package deployer

import (
	"fmt"
	"sort"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// modeGuard refuses to provision anything for a Collector that is not in automatic mode.
//
// Belt over braces, and the braces are stronger: the coordinator wires api.CollectorDeployer only for
// a type in spec.ActiveCollectorDeploymentTypes, so a manual-mode Collector reaches no mutating
// interface anywhere in the runtime. This check exists because THIS package creates IAM roles and
// secrets, and "the caller could not have been reached" is a worse guarantee to rely on than "the
// function refuses".
func modeGuard(mode string) []collectorstatus.Problem {
	if mode == spec.ModeAutomatic {
		return nil
	}
	return []collectorstatus.Problem{{
		Code:     ProblemManualModeProvisioning,
		Severity: collectorstatus.SeverityError,
		Message: fmt.Sprintf("refusing to provision Collector identity in %q mode: Odigos creates no roles, "+
			"log groups or secrets for a Collector the customer owns", mode),
	}}
}

// notOurs is the refusal for an object whose name Odigos needs and whose ownership it cannot claim.
//
// Refused rather than adopted, and rather than renamed around. Adopting would mean tagging and
// rewriting the policy of something a customer built; renaming would mean the object a customer sees in
// their console is no longer the one the documentation names, and the conflict would stay there for
// ever. So Odigos stops, says exactly which object and why, and leaves it untouched.
//
// The message names the class it fell into, because the three cases have different fixes: an object
// with no Odigos tags is probably the customer's own, one tagged for another connector means two
// connectors share a name in one account, and a customer-named one is configuration.
func notOurs(kind, name string, object collectorownership.Object, self collectorownership.Identity) collectorstatus.Problem {
	class := collectorownership.Classify(object, self, spec.ModeAutomatic)
	detail := "it carries no Odigos tags, so it is not something Odigos created"
	switch class.Attribution {
	case collectorownership.AttributionForeign:
		detail = fmt.Sprintf("it is tagged for connector %q, so two connectors are using one AWS account with "+
			"names that collide here", object.Tags.Connector)
	case collectorownership.AttributionNone:
		detail = "it is not a Collector object at all"
	}
	return collectorstatus.Problem{
		Code:     ProblemObjectNotOurs,
		Severity: collectorstatus.SeverityError,
		Message: fmt.Sprintf("the %s %q already exists and %s. Odigos will not change it, so no Collector is "+
			"provisioned: remove that object, or rename the connector so its objects do not collide",
			kind, name, detail),
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
