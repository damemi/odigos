// Package detector implements WORKLOAD discovery for the MuleSoft connector: Mule applications on CloudHub 2.0
// and Runtime Fabric (from the Application Manager API) and on hybrid servers (from Runtime Manager).
package detector

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/odigos-io/odigos/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

var log = connlog.Subsystem("mulesoft-workload-detector")

// inventory is the slice of the workload store the detectors write. An interface so detection is testable
// against the fake Anypoint API without a database.
type inventory interface {
	UpsertApplication(ctx context.Context, app store.Application) error
	KnownTargets(ctx context.Context) (map[string]store.KnownTarget, error)
	Now(ctx context.Context) (time.Time, error)
	UpdateDiscoveryState(ctx context.Context, environmentID, kind string, scanAt time.Time) error
	Prune(ctx context.Context, kind string, before time.Time, scope store.PruneScope) error
	PurgeDeleted(ctx context.Context, before time.Time) error
}

// scanned is one Application Manager deployment, read in full, with the environment and target it belongs to.
type scanned struct {
	env    anypoint.Environment
	dep    anypoint.Deployment
	target anypoint.Target
	kind   string
	// guessed is true when the deployment's target was not in the targets list (or the list could not be
	// read), so kind and target come from the target id's shape rather than from the platform.
	guessed bool
	// apis are the API Manager instances of the deployment's environment; apisRead is false when they could
	// not be listed, and the application's recorded pairing is then left as it was.
	apis     []anypoint.APIInstance
	apisRead bool
}

// scanResult is one pass over every visible environment.
type scanResult struct {
	envs        []anypoint.Environment
	deployments []scanned
	// listComplete is false when the environment list itself was truncated.
	listComplete bool
	// readEnvs holds the environments whose every deployment was read. Completeness is per environment: an
	// environment the Connected App has no scope in fails every cycle, and must not stop applications
	// undeployed elsewhere from being pruned.
	readEnvs map[string]bool
}

// AMCScanner lists every Application Manager deployment in the organization once per discovery cycle and
// shares the result between the CloudHub 2.0 and Runtime Fabric detectors. The base runs one detector per
// enabled type, and both types come from the same API, so without sharing a connector with both enabled would
// read every deployment twice per cycle against a per-organization rate limit.
type AMCScanner struct {
	client *anypoint.Client
	orgID  string
	// reuse is how long a completed scan is served to the next caller. It must be shorter than the discovery
	// interval, so it only ever joins the two detectors of one cycle.
	reuse time.Duration

	mu   sync.Mutex
	at   time.Time
	last scanResult
}

// NewAMCScanner builds a scanner for an organization.
func NewAMCScanner(client *anypoint.Client, orgID string) *AMCScanner {
	return &AMCScanner{client: client, orgID: orgID, reuse: 30 * time.Second}
}

// Scan returns the organization's deployments, reusing a scan finished within the reuse window.
func (s *AMCScanner) Scan(ctx context.Context) (scanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < s.reuse {
		return s.last, nil
	}
	res, err := s.scan(ctx)
	if err != nil {
		return scanResult{}, err
	}
	s.at, s.last = time.Now(), res
	return res, nil
}

func (s *AMCScanner) scan(ctx context.Context) (scanResult, error) {
	envs, err := s.client.Environments(ctx, s.orgID)
	truncated := errors.Is(err, anypoint.ErrIncomplete)
	if err != nil && !truncated {
		return scanResult{}, err
	}
	// A truncated environment list still lets the visible environments be scanned; it only rules out pruning
	// environments missing from it.
	res := scanResult{envs: envs, listComplete: !truncated, readEnvs: map[string]bool{}}
	if truncated {
		log.Warn("environment list is incomplete; discovering what was returned without pruning", "error", err)
	}

	// Targets classify a deployment as CloudHub 2.0 or Runtime Fabric. Reading them needs a scope a
	// CloudHub-only connector may lack, so a failure degrades classification instead of failing discovery.
	targets := map[string]anypoint.Target{}
	if ts, err := s.client.Targets(ctx, s.orgID); err != nil {
		log.Warn("list deployment targets failed; classifying deployments by target id", "error", anypoint.Summarize(err))
	} else {
		for _, t := range ts {
			targets[t.ID] = t
		}
	}

	for _, env := range envs {
		list, err := s.client.ListDeployments(ctx, s.orgID, env.ID)
		read := true
		if errors.Is(err, anypoint.ErrIncomplete) {
			log.Warn("deployment list is incomplete; discovering what was returned without pruning", "environment", env.Name, "error", err)
			read, err = false, nil
		}
		if err != nil {
			log.Error("list deployments failed", "environment", env.Name, "error", anypoint.Summarize(err))
			continue
		}
		// API Manager pairs applications with API instances (autodiscovery). Reading it needs an API Manager scope
		// a connector may lack, so a failure only leaves the recorded pairings as they were.
		var apis []anypoint.APIInstance
		apisRead := false
		if len(list) > 0 {
			if apis, err = s.client.ListAPIInstances(ctx, s.orgID, env.ID); err != nil {
				log.Debug("list API Manager instances failed; keeping recorded API pairings", "environment", env.Name, "error", anypoint.Summarize(err))
			} else {
				apisRead = true
			}
		}
		for _, summary := range list {
			// The list view is a summary; properties and the runtime version need the full deployment.
			dep, err := s.client.GetDeployment(ctx, s.orgID, env.ID, summary.ID)
			if err != nil {
				log.Error("get deployment failed", "environment", env.Name, "deployment", summary.Name, "error", anypoint.Summarize(err))
				read = false
				continue
			}
			target, kind, guessed := classify(dep.Target.TargetID, targets)
			res.deployments = append(res.deployments, scanned{env: env, dep: dep, target: target, kind: kind, guessed: guessed, apis: apis, apisRead: apisRead})
		}
		res.readEnvs[env.ID] = read
	}
	return res, nil
}

// classify resolves a deployment's target and workload kind.
//
// Only a target known to be a shared space is CloudHub 2.0: the targets list says so, or, for a target missing
// from the list, its id has the shared-space shape ("cloudhub-<region>"). That kind is the one Odigos
// instruments, so anything it cannot place is kept out of it. A target the list calls a fabric is Runtime
// Fabric; any other target — a private space, a type the list spells in a way this does not recognize, or a
// bare UUID missing from the list — is reported as a private space, the more common of the two UUID-shaped
// targets. guessed marks every answer not taken from the targets list; the detector prefers what it recorded for
// the deployment before (see resolveKinds).
func classify(targetID string, targets map[string]anypoint.Target) (anypoint.Target, string, bool) {
	if t, ok := targets[targetID]; ok {
		switch t.Class() {
		case anypoint.TargetRuntimeFab:
			return t, store.KindRTF, false
		case anypoint.TargetSharedSpace:
			return t, store.KindCloudHub2, false
		case anypoint.TargetPrivateSpace:
			return t, store.KindPrivateSpace, false
		}
		if isSharedSpaceID(targetID) {
			return t, store.KindCloudHub2, false
		}
		return t, store.KindPrivateSpace, false
	}
	t := anypoint.Target{ID: targetID, Name: targetID, Type: anypoint.TargetUnknown}
	if isSharedSpaceID(targetID) {
		t.Type = anypoint.TargetSharedSpace
		t.Region = strings.TrimPrefix(targetID, "cloudhub-")
		return t, store.KindCloudHub2, true
	}
	return t, store.KindPrivateSpace, true
}

// isSharedSpaceID reports whether a target id has the shape CloudHub 2.0 gives its shared spaces.
func isSharedSpaceID(id string) bool { return strings.HasPrefix(id, "cloudhub-") }

func toApplication(s scanned) store.Application {
	meta := map[string]any{
		"applicationStatus": s.dep.Application.Status,
		"desiredState":      s.dep.Application.DesiredState,
		"replicas":          s.dep.Target.Replicas,
	}
	if v, ok := anypoint.ParseMuleVersion(s.dep.RuntimeVersion()); ok && v.Java != "" {
		meta["javaVersion"] = v.Java
	}
	if s.apisRead {
		meta[store.MetaAPI] = PairedAPI(s.dep.Properties(), s.apis)
	}
	return store.Application{
		ExternalID:      s.dep.ID,
		Kind:            s.kind,
		AppName:         s.dep.Name,
		EnvironmentID:   s.env.ID,
		EnvironmentName: s.env.Name,
		TargetID:        s.target.ID,
		TargetName:      s.target.Name,
		TargetType:      s.target.Class(),
		TargetRegion:    s.target.Region,
		RuntimeVersion:  s.dep.RuntimeVersion(),
		Status:          s.dep.Status,
		Origin:          store.MarkerOrigin(s.dep.Properties(), s.dep.SecurePropertyKeys(), s.dep.ID),
		Metadata:        meta,
	}
}

// AMCDetector discovers the deployments of ONE kind (CloudHub 2.0 shared space, private space or Runtime Fabric)
// from the shared scan, so each enabled type persists and prunes only its own rows.
type AMCDetector struct {
	kind    string
	scanner *AMCScanner
	store   inventory
}

// NewAMCDetector builds the detector for one Application Manager kind.
func NewAMCDetector(kind string, scanner *AMCScanner, st inventory) *AMCDetector {
	return &AMCDetector{kind: kind, scanner: scanner, store: st}
}

func (d *AMCDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	start, clockOK := scanStart(ctx, d.store)
	res, err := d.scanner.Scan(ctx)
	if err != nil {
		log.Error("mule application scan failed", "kind", d.kind, "error", anypoint.Summarize(err))
		return baseapi.DetectResult{Authenticated: !anypoint.IsAuthErr(err)}, err
	}

	deployments, err := resolveKinds(ctx, d.store, res.deployments)
	if err != nil {
		// Without the recorded classification a guess could relabel a Runtime Fabric app as CloudHub 2.0, so
		// this cycle writes nothing rather than risk it.
		log.Error("read recorded deployment targets failed; skipping this cycle", "kind", d.kind, "error", err)
		return baseapi.DetectResult{Authenticated: true}, err
	}
	read := maps.Clone(res.readEnvs)
	count := 0
	for _, s := range deployments {
		if s.kind != d.kind {
			continue
		}
		app := toApplication(s)
		if err := d.store.UpsertApplication(ctx, app); err != nil {
			log.Error("upsert mule application failed", "app", app.AppName, "environment", app.EnvironmentName, "error", err)
			read[s.env.ID] = false
			continue
		}
		count++
	}
	for _, env := range res.envs {
		if read[env.ID] {
			_ = d.store.UpdateDiscoveryState(ctx, env.ID, d.kind, start)
		}
	}
	scope := pruneScope(res.envs, read, res.listComplete, clockOK)
	finish(ctx, d.store, d.kind, scope, start)
	log.Info("mule application discovery complete", "kind", d.kind, "count", count, "environmentsRead", len(scope.Complete), "environments", len(res.envs))
	return baseapi.DetectResult{Authenticated: true}, nil
}

// pruneScope is what a scan can vouch for: the environments read in full, and — when the environment list was
// complete — every environment that still exists. Nothing at all without the database clock.
func pruneScope(envs []anypoint.Environment, read map[string]bool, listComplete, clockOK bool) store.PruneScope {
	var scope store.PruneScope
	if !clockOK {
		return scope
	}
	for _, env := range envs {
		if read[env.ID] {
			scope.Complete = append(scope.Complete, env.ID)
		}
	}
	if listComplete {
		scope.Listed = make([]string, 0, len(envs))
		for _, env := range envs {
			scope.Listed = append(scope.Listed, env.ID)
		}
	}
	return scope
}

// resolveKinds keeps the classification recorded for a deployment in two cases. Every AMC detector resolves
// from the same rows, so they agree.
//
// A guessed classification: a guess happens whenever the targets list is unreadable — a missing scope, or one
// failed call. Taken as truth, a single failed call would relabel every Runtime Fabric app as CloudHub 2.0: its
// Runtime Fabric source would stop matching, the CloudHub 2.0 reconciler would roll the app back, and the next
// good scan would instrument it again — two redeploys for nothing. A deployment's target type does not change,
// so what was recorded from the platform earlier is the better answer. Only a deployment seen for the first
// time while the list is unreadable keeps the guess.
//
// A deployment Odigos instrumented under a kind it now classifies differently (first seen while the targets
// list was unreadable, say): moving the row would take it away from the reconciler that owns its marker — and
// if the new kind is not enabled, or is in manual mode, nothing would ever roll that marker back, the connector
// delete drain included. The row keeps its kind until the marker is gone, then moves.
func resolveKinds(ctx context.Context, st inventory, in []scanned) ([]scanned, error) {
	needed := false
	for _, s := range in {
		if s.guessed || ownedByConnector(s) {
			needed = true
			break
		}
	}
	if !needed {
		return in, nil
	}
	known, err := st.KnownTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]scanned, len(in))
	for i, s := range in {
		k, ok := known[s.dep.ID]
		switch {
		case !ok:
		case s.guessed:
			s = withRecorded(s, k)
		case k.Kind != s.kind && ownedByConnector(s):
			log.Warn("keeping an instrumented application under its recorded kind until Odigos rolls it back",
				"app", s.dep.Name, "environment", s.env.Name, "recorded", k.Kind, "classified", s.kind)
			s = withRecorded(s, k)
		}
		out[i] = s
	}
	return out, nil
}

func ownedByConnector(s scanned) bool {
	return store.MarkerOrigin(s.dep.Properties(), s.dep.SecurePropertyKeys(), s.dep.ID) == store.OriginConnector
}

func withRecorded(s scanned, k store.KnownTarget) scanned {
	s.kind = k.Kind
	s.target = anypoint.Target{ID: s.target.ID, Name: k.TargetName, Type: k.TargetType, Region: k.TargetRegion}
	return s
}

// scanStart returns the prune cutoff from the DATABASE clock. Rows record last_seen_at with Postgres's now(),
// so a cutoff from the connector's own clock compares two clocks: with Postgres even a little behind the pod,
// rows upserted right after the cutoff look older than it and are pruned. That is not hypothetical here — the
// Runtime Fabric detector reuses the CloudHub 2.0 scan, so its upserts follow its start by microseconds, and a
// pruned row reads as an undeployed application, which the reconciler then rolls back. ok is false when the
// clock cannot be read; the caller then does not prune.
func scanStart(ctx context.Context, st inventory) (time.Time, bool) {
	t, err := st.Now(ctx)
	if err != nil {
		log.Warn("read database clock failed; this cycle will not prune", "error", err)
		return time.Now(), false
	}
	return t, true
}

// finish prunes the rows the scan can vouch are gone (see store.PruneScope) and purges long-deleted rows.
func finish(ctx context.Context, st inventory, kind string, scope store.PruneScope, start time.Time) {
	if scope.Empty() {
		log.Warn("skipping prune: no environment was read in full", "kind", kind)
	} else if err := st.Prune(ctx, kind, start, scope); err != nil {
		log.Error("prune mule applications failed", "kind", kind, "error", err)
	}
	if err := st.PurgeDeleted(ctx, time.Now().Add(-store.SoftDeleteRetention)); err != nil {
		log.Error("purge deleted mule applications failed", "error", err)
	}
}
