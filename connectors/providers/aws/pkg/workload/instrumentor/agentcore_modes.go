package instrumentor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
)

// Automatic AgentCore instrumentation adopts the OpenTelemetry SDK the agent's artifact already carries and
// configures it through the runtime's environment (and, for a ZIP, its entry point). It never rewrites or
// republishes the artifact. agentCoreSDKKind is which carried SDK adoption starts.
type agentCoreSDKKind string

const (
	// agentCoreSDKOdigos is the Odigos package (odigos-agentcore / @odigos/agentcore, or a prepared carrier
	// bundle). It is Odigos code: it roots each invocation at the agent, links AgentCore's span, names server
	// spans by route and redacts GenAI content, and it can start either profile it was built with.
	agentCoreSDKOdigos agentCoreSDKKind = "odigos"
	// agentCoreSDKADOT is AWS's distribution, started the way AWS documents for AgentCore.
	agentCoreSDKADOT agentCoreSDKKind = "adot"
)

// agentCoreAdopt is the delivery every automatic installation records in the preparation contract.
const agentCoreAdopt = "adopt"

// agentCorePackageSDK reads which SDK a preparation contract declares. Contracts written before the SDK was
// recorded say "adopt" for ADOT and nothing for the Odigos package; manual-mode preparation still writes them so.
func agentCorePackageSDK(p agentcore.Package) agentCoreSDKKind {
	switch {
	case p.SDK != "":
		return agentCoreSDKKind(p.SDK)
	case p.Mode == agentCoreAdopt:
		return agentCoreSDKADOT
	default:
		return agentCoreSDKOdigos
	}
}

// agentCoreSnapshotSDK reads the SDK an installation snapshot recorded. Snapshots written before the SDK was
// recorded hold the earlier delivery names: "adopt" for ADOT, "package" or nothing for the Odigos package.
func agentCoreSnapshotSDK(s agentCoreSnapshot) agentCoreSDKKind {
	switch {
	case s.SDK != "":
		return s.SDK
	case s.Mode == agentCoreAdopt:
		return agentCoreSDKADOT
	default:
		return agentCoreSDKOdigos
	}
}

// agentCoreWaiting is a runtime whose artifact carries no SDK Odigos can configure. Nothing is broken: the
// customer's next deployment resolves it, so it is reported as a wait with the one-line fix, not as an error.
type agentCoreWaiting struct{ Hint string }

// agentCoreDistroDependency names the dependency whose build installs the SDK a distro starts.
func agentCoreDistroDependency(distro string) string {
	if extra, ok := strings.CutPrefix(distro, "python-agentcore-"); ok {
		return "odigos-agentcore[" + extra + "]"
	}
	return "@odigos/agentcore"
}

// agentCoreDistroWaiting reports a selected distro the artifact cannot start. The artifact carries the SDKs its
// build installed, so a selection outside them is a wait for the deployment that adds one, not a refusal: the
// hint names that dependency and the distro the artifact could start instead.
func agentCoreDistroWaiting(selected string, carried []string) *agentCoreWaiting {
	if selected == "" || slices.Contains(carried, selected) {
		return nil
	}
	hint := "The selected distribution " + selected + " is not in the agent's artifact"
	if len(carried) > 0 {
		hint += ", which carries " + strings.Join(carried, " and ")
	}
	hint += ". Add " + agentCoreDistroDependency(selected) + " to the agent's dependencies and redeploy"
	if len(carried) > 0 {
		hint += ", or select " + carried[0]
	}
	return &agentCoreWaiting{Hint: hint + "."}
}

// agentCoreOdigosDistros are the profiles the carried Odigos package (or prepared bundle) can start.
func agentCoreOdigosDistros(f agentCoreArtifactFacts) []string {
	switch {
	case f.OdigosPackage != nil:
		return f.OdigosPackage.Distros
	case f.PreparedBundle:
		return f.BundlePackage.Distros
	}
	return nil
}

func agentCoreCarriesOdigos(f agentCoreArtifactFacts) bool {
	return f.OdigosPackage != nil || f.PreparedBundle
}

// agentCoreOdigosRunnable reports why the carried Odigos package cannot start in this agent: an error for a
// layout Odigos code cannot own, a wait for one the customer's next deployment fixes, both nil when it can.
func agentCoreOdigosRunnable(f agentCoreArtifactFacts) (*agentCoreWaiting, error) {
	python := f.Language == "python"
	// The package's image route starts through a .pth file, which coexists with the application's own
	// sitecustomize; the ZIP wrapper and the carrier bundle start through a sitecustomize of their own.
	if python && f.Sitecustomize && (!f.Image || f.PreparedBundle) {
		return nil, fmt.Errorf("%w: existing Python sitecustomize requires explicit integration", ErrUnsupportedInstrumentation)
	}
	if !python && f.ESM {
		return nil, fmt.Errorf("%w: native ESM is not qualified for the Odigos package", ErrUnsupportedInstrumentation)
	}
	// Two SDKs would start: the customer's launcher and the package's bootstrap. A ZIP's launcher entry point
	// prefix is replaced by the wrapper instead; NODE_OPTIONS and an image command cannot be.
	if f.LauncherPresent && (f.Image || f.LauncherInEnv) {
		if python {
			return nil, fmt.Errorf("%w: the image starts through opentelemetry-instrument; remove the launcher, the Odigos package starts the SDK", ErrUnsupportedInstrumentation)
		}
		return nil, fmt.Errorf("%w: the runtime already requires the ADOT register hook; remove it, the Odigos package starts the SDK", ErrUnsupportedInstrumentation)
	}
	if f.UpstreamPreload {
		return nil, fmt.Errorf("%w: the runtime already requires @opentelemetry/auto-instrumentations-node/register; remove it, the Odigos package starts the SDK", ErrUnsupportedInstrumentation)
	}
	if python && !f.PreparedBundle && len(f.OdigosPackage.Distros) == 0 {
		return &agentCoreWaiting{Hint: "Install odigos-agentcore[adot] (or odigos-agentcore[openllmetry]) instead of odigos-agentcore alone and redeploy: without an extra the package carries no OpenTelemetry SDK."}, nil
	}
	if !f.Image {
		pkg := "odigos-agentcore"
		if !python {
			pkg = "@odigos/agentcore"
		}
		if f.StartWrapper == "" {
			wrapper := agentCorePythonStart
			if !python {
				wrapper = agentCoreNodePackageStart
			}
			return &agentCoreWaiting{Hint: "The ZIP carries " + pkg + " without its start wrapper at " + wrapper + ". Install the package at the ZIP root and redeploy."}, nil
		}
		if !f.StartWrapperReadsEnv {
			return &agentCoreWaiting{Hint: "The " + pkg + " start wrapper in this ZIP is from an earlier release that AgentCore cannot start. Upgrade " + pkg + " (or the carrier bundle) and redeploy."}, nil
		}
	}
	return nil, nil
}

// agentCoreADOTRunnable reports a wait when the carried ADOT cannot be started from the runtime's configuration.
func agentCoreADOTRunnable(f agentCoreArtifactFacts) *agentCoreWaiting {
	if f.Language == "python" && f.Image && !f.LauncherPresent {
		// The ADOT distro only starts through its launcher; environment cannot start it in an image.
		return &agentCoreWaiting{Hint: "Start the agent through opentelemetry-instrument in the image's CMD, or add odigos-agentcore[adot] to the agent's dependencies, and redeploy."}
	}
	return nil
}

// chooseAgentCoreSDK picks which carried SDK adoption starts:
//
//  1. the source's distro override, through whichever carried SDK can start it (the Odigos package first);
//  2. otherwise the Odigos package, the best instrumentation an agent can carry;
//  3. otherwise ADOT, also when the agent carries the Odigos package in a layout it cannot start in.
//
// An artifact with neither waits: Odigos never rewrites or republishes the customer's artifact.
func chooseAgentCoreSDK(f agentCoreArtifactFacts, override string) (agentCoreSDKKind, *agentCoreWaiting, error) {
	python := f.Language == "python"
	adotDistro := f.Language + "-agentcore-adot"
	var odigosWait *agentCoreWaiting
	var odigosErr error
	if agentCoreCarriesOdigos(f) {
		odigosWait, odigosErr = agentCoreOdigosRunnable(f)
	}
	odigosOK := agentCoreCarriesOdigos(f) && odigosWait == nil && odigosErr == nil
	adotWait := agentCoreADOTRunnable(f)
	adotOK := f.ADOT != nil && adotWait == nil

	if override != "" {
		odigosCarries := slices.Contains(agentCoreOdigosDistros(f), override)
		switch {
		case odigosCarries && odigosOK:
			return agentCoreSDKOdigos, nil, nil
		case override == adotDistro && adotOK:
			return agentCoreSDKADOT, nil, nil
		case odigosCarries:
			return "", odigosWait, odigosErr
		case override == adotDistro && f.ADOT != nil:
			return "", adotWait, nil
		}
		var carried []string
		if odigosOK {
			carried = append(carried, agentCoreOdigosDistros(f)...)
		}
		if adotOK && !slices.Contains(carried, adotDistro) {
			carried = append(carried, adotDistro)
		}
		return "", agentCoreDistroWaiting(override, carried), nil
	}
	switch {
	case odigosOK:
		return agentCoreSDKOdigos, nil, nil
	case adotOK:
		return agentCoreSDKADOT, nil, nil
	case agentCoreCarriesOdigos(f):
		return "", odigosWait, odigosErr
	case f.ADOT != nil:
		return "", adotWait, nil
	}
	hint := "Add odigos-agentcore[adot] to the agent's dependencies and redeploy."
	if !python {
		hint = "Add @odigos/agentcore to the agent's dependencies and redeploy."
	}
	if f.OtherSDK {
		hint = "The installed OpenTelemetry distribution is not ADOT and cannot be adopted. " + hint
	}
	return "", &agentCoreWaiting{Hint: hint}, nil
}
