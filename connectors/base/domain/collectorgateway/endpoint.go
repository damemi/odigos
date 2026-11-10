package collectorgateway

import (
	"errors"
	"fmt"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos/connectors/opamp"
	"github.com/odigos-io/odigos/connectors/spec"
)

// CheckEndpoint reports whether this connector can point a Collector at Central.
//
// Wraps connectors/opamp — the rule connector-runtime validates against when it stamps the endpoint —
// into the status vocabulary, so the connector reports the same verdict the controller reported rather
// than a second opinion about the same string.
//
// Only reported when a Collector was actually asked for. An installation that does not use managed
// Collectors has no use for the endpoint, and reporting a fault on every connector in it is how an
// operator learns to ignore the report that matters.
func CheckEndpoint(configured string, collectorTypes []spec.Type) []collectorstatus.Problem {
	if len(collectorTypes) == 0 {
		return nil
	}
	if _, err := opamp.Resolve(configured); err != nil {
		return []collectorstatus.Problem{endpointProblem(err)}
	}
	return nil
}

func endpointProblem(err error) collectorstatus.Problem {
	if errors.Is(err, opamp.ErrNotConfigured) {
		return collectorstatus.Problem{
			Code:     "OpAMPEndpointNotConfigured",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("this connector may manage a Collector, but %s is not set, so it has nowhere "+
				"to point one and will deploy none. Set the public OpAMP endpoint on the Central installation "+
				"(normally wss://<central-host>%s); connector-runtime stamps it from there",
				opamp.EnvEndpoint, opamp.DefaultPath),
		}
	}
	return collectorstatus.Problem{
		Code:     "OpAMPEndpointInvalid",
		Severity: collectorstatus.SeverityError,
		Message:  fmt.Sprintf("the public OpAMP endpoint cannot be used, so no Collector will be deployed: %v", err),
	}
}

// CheckEndpointDrift reports a Collector still carrying an endpoint the installation has moved away
// from.
//
// A Collector holds the endpoint injected into its workload definition at deploy time, exactly as it
// holds its credential - so moving Central does not move the Collectors already out there.
// Until each is redeployed its configuration plane is down: it keeps exporting telemetry under its last
// configuration, and Odigos can no longer change it.
//
// Reported rather than silently converged because the two modes cure it differently, and the difference
// has a visible cost. `deployed` empty means nothing is deployed yet, which is not drift.
func CheckEndpointDrift(deployed, configured, mode string) []collectorstatus.Problem {
	if !opamp.Changed(deployed, configured) {
		return nil
	}
	if mode == spec.ModeAutomatic {
		return []collectorstatus.Problem{{
			Code:     "OpAMPEndpointMovedRedeployPending",
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("this Collector was deployed against %s and the installation now publishes "+
				"%s; Odigos is replacing its tasks to pick up the new endpoint. Its configuration plane is down "+
				"until that lands, and a Collector on ephemeral storage loses its queued telemetry when tasks "+
				"are replaced", deployed, configured),
		}}
	}
	return []collectorstatus.Problem{{
		Code:     "OpAMPEndpointMovedAwaitingCustomer",
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("this Collector was deployed against %s and the installation now publishes %s. "+
			"Odigos does not modify your infrastructure, so redeploy it with the new endpoint; until then Odigos "+
			"cannot change its configuration and it keeps running on the last one it received",
			deployed, configured),
	}}
}
