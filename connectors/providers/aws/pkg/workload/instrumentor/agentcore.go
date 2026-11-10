package instrumentor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	dest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/awsclient"
)

type agentCoreStore interface {
	ListAgentCore(context.Context) ([]agentcore.Runtime, error)
	UpsertAgentCore(context.Context, agentcore.Runtime) error
	ListSources(context.Context) ([]basestore.SourceRecord, error)
	OTLPExportTarget(context.Context) (dest.OTLPTarget, bool, error)
	OTLPExportHeaders(context.Context) (map[string]string, error)
	SetAgentCoreState(context.Context, string, map[string]any) error
	AgentCoreState(context.Context, string) (map[string]any, error)
	SaveAgentCoreSnapshot(context.Context, string, string, []byte) error
	LoadAgentCoreSnapshot(context.Context, string, string) ([]byte, error)
}

// AgentCore installs qualified runtime artifacts and configures global OTLP export. It has no collector API,
// route resolver or health input: collector failures cannot change application instrumentation.
type AgentCore struct {
	store     agentCoreStore
	client    agentcore.Factory
	installer agentCoreInstaller
	// waiting remembers, per runtime ARN, what was inspected when its artifact was last found to carry no SDK.
	// Detection reads the ZIP's index or the image's layers, and a runtime can wait for weeks; until its deployment
	// or source configuration changes, the answer cannot change either.
	mu      sync.Mutex
	waiting map[string]agentCoreWaitingEntry
	// confirmFor bounds how long a pass waits, after updating a runtime, for DEFAULT to serve the new version so
	// the runtime reads as instrumented in the same pass. Zero leaves confirmation to the next pass.
	confirmFor, confirmEvery time.Duration
}

var agentCoreLog = connlog.Subsystem("aws-agentcore")

type agentCoreWaitingEntry struct{ key, hint string }

// agentCoreArtifactNote is the routing note code carrying a waiting runtime's one-line fix.
const agentCoreArtifactNote = "agentcore-artifact-change"

func NewAgentCore(st agentCoreStore, cache *awsclient.Cache) *AgentCore {
	return &AgentCore{store: st, client: agentcore.NewFactory(cache), installer: &agentCoreArtifactInstaller{io: newAgentCoreAWSArtifacts(cache)},
		confirmFor: 3 * time.Minute, confirmEvery: 10 * time.Second}
}

func (r *AgentCore) Reconcile(ctx context.Context) error { return r.run(ctx, false) }

// AgentCoreObserver deliberately exposes only Observe to the coordinator's manual-mode registry.
// Its execution never enters the mutation branch, including when a connector-owned marker exists.
type AgentCoreObserver struct{ runtime *AgentCore }

func NewAgentCoreObserver(st agentCoreStore, cache *awsclient.Cache) *AgentCoreObserver {
	return &AgentCoreObserver{runtime: NewAgentCore(st, cache)}
}
func (o *AgentCoreObserver) Observe(ctx context.Context) error { return o.runtime.run(ctx, true) }

func (r *AgentCore) run(ctx context.Context, observe bool) error {
	sources, err := r.store.ListSources(ctx)
	if err != nil {
		return err
	}
	enabled := map[string]basestore.SourceRecord{}
	for _, s := range sources {
		if s.Kind == agentcore.Kind && s.Enabled {
			enabled[s.ExternalResourceID] = s
		}
	}
	runtimes, err := r.store.ListAgentCore(ctx)
	if err != nil {
		return err
	}
	target, found, targetErr := r.store.OTLPExportTarget(ctx)
	if targetErr == nil && found {
		targetErr = agentCoreTarget(target)
	}
	if targetErr == nil && !found {
		targetErr = fmt.Errorf("configure a global OTLP/HTTP destination; AgentCore managed collector routing is deferred")
	}
	headers, headerErr := r.store.OTLPExportHeaders(ctx)
	if headerErr != nil {
		targetErr = headerErr
	}
	started := time.Now()
	budget := &agentCoreBudget{left: agentCoreUpdatesPerPass}
	errs := make([]error, len(runtimes))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(agentCoreConcurrency, len(runtimes)) {
		workers.Go(func() {
			for i := range jobs {
				errs[i] = r.runOne(ctx, runtimes[i], enabled, observe, target, targetErr, headers, budget)
			}
		})
	}
	for i := range runtimes {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	agentCoreLog.Debug("agentcore pass complete", "observe", observe, "runtimes", len(runtimes),
		"updates", agentCoreUpdatesPerPass-budget.remaining(), "duration", time.Since(started).String())
	return errors.Join(errs...)
}

const (
	// agentCoreUpdatesPerPass caps how many runtimes one pass may change.
	agentCoreUpdatesPerPass = 5
	// agentCoreConcurrency is how many runtimes one pass processes at once. Each reads its artifact and may wait
	// for its DEFAULT endpoint, so a pass over several redeployed agents is not their sum.
	agentCoreConcurrency = 4
)

// runOne reconciles or observes one runtime and records its outcome on the workload.
func (r *AgentCore) runOne(ctx context.Context, record agentcore.Runtime, enabled map[string]basestore.SourceRecord, observe bool,
	target dest.OTLPTarget, targetErr error, headers map[string]string, budget *agentCoreBudget) error {
	src, want := enabled[record.ARN]
	if !want && record.Origin == "" {
		return nil
	}
	if want && targetErr != nil {
		var err error
		if observe {
			err = r.setState(ctx, record.ARN, map[string]any{"observation_error": targetErr.Error()})
		} else {
			err = r.setError(ctx, record.ARN, targetErr)
		}
		return err
	}
	slot := budget.slot()
	defer slot.release()
	// Reading a large image's layers from outside AWS can take minutes; the layer cache makes a retry quick.
	operationCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	err := r.process(operationCtx, record, src, want, observe, target, headers, slot)
	cancel()
	if err == nil {
		return nil
	}
	if observe {
		if strings.HasPrefix(err.Error(), baseapi.ErrPrefixUnsupported) {
			_ = r.setError(ctx, record.ARN, err)
		} else {
			_ = r.setState(ctx, record.ARN, map[string]any{"observation_error": err.Error()})
		}
	} else {
		_ = r.setError(ctx, record.ARN, err)
	}
	return fmt.Errorf("AgentCore %s: %w", record.Name, err)
}

// agentCoreBudget is the number of runtime updates left in one pass, shared by the runtimes it processes at once.
type agentCoreBudget struct {
	mu   sync.Mutex
	left int
}

func (b *agentCoreBudget) slot() *agentCoreSlot { return &agentCoreSlot{budget: b} }

func (b *agentCoreBudget) remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.left
}

// agentCoreSlot is one runtime's claim on the pass budget. take reserves an update before the runtime is changed;
// release returns it if the runtime ended up unchanged, so concurrent runtimes never exceed the budget.
type agentCoreSlot struct {
	budget      *agentCoreBudget
	held, spent bool
}

func (s *agentCoreSlot) take() bool {
	if s.held {
		return true
	}
	if s.budget == nil {
		return false
	}
	s.budget.mu.Lock()
	defer s.budget.mu.Unlock()
	if s.budget.left <= 0 {
		return false
	}
	s.budget.left--
	s.held = true
	return true
}

func (s *agentCoreSlot) spend() { s.spent = true }

func (s *agentCoreSlot) release() {
	if !s.held || s.spent || s.budget == nil {
		return
	}
	s.budget.mu.Lock()
	defer s.budget.mu.Unlock()
	s.budget.left++
	s.held = false
}
func (r *AgentCore) setError(ctx context.Context, arn string, err error) error {
	return r.setState(ctx, arn, map[string]any{"instrumentation_error": err.Error(), "instrumentation_phase": "", "settling": false})
}

// setState is the one writer of a runtime's reported state. Every write clears the artifact wait unless it sets
// one, so a wait cannot outlive the reconcile that found it; the wait's note goes with it.
func (r *AgentCore) setState(ctx context.Context, arn string, values map[string]any) error {
	if _, ok := values["waiting_for"]; !ok {
		values["waiting_for"] = ""
		if _, ok := values["routing_note"]; !ok && r.showsArtifactNote(ctx, arn) {
			values["routing_note"] = nil
		}
	}
	return r.store.SetAgentCoreState(ctx, arn, values)
}

// showsArtifactNote reports whether the stored routing note is a wait's hint. Other notes (endpoint coverage)
// belong to the observation that wrote them and are left to it.
func (r *AgentCore) showsArtifactNote(ctx context.Context, arn string) bool {
	state, err := r.store.AgentCoreState(ctx, arn)
	if err != nil || state["routing_note"] == nil {
		return false
	}
	raw, err := json.Marshal(state["routing_note"])
	if err != nil {
		return false
	}
	var note baseapi.WorkloadRoutingNote
	return json.Unmarshal(raw, &note) == nil && note.Code == agentCoreArtifactNote
}

// setWaiting reports a runtime whose artifact carries no SDK Odigos can configure. It is a wait, not an error:
// nothing is broken, and the hint names the dependency the customer's next deployment adds.
func (r *AgentCore) setWaiting(ctx context.Context, arn, hint string, settling bool) error {
	return r.setState(ctx, arn, map[string]any{"waiting_for": baseapi.WaitingForArtifact, "instrumentation_phase": "", "instrumentation_error": "",
		"settling": settling, "routing_note": &baseapi.WorkloadRoutingNote{Code: agentCoreArtifactNote, Message: hint}})
}

// agentCoreWaitingKey identifies what a detection inspected: the deployed version (AgentCore takes the code at
// deployment, so an artifact rewritten in place is not live until the next one), the artifact and the source's
// configuration.
func agentCoreWaitingKey(live *ac.GetAgentRuntimeOutput, src basestore.SourceRecord) string {
	artifact, _ := agentCoreArtifact(live.AgentRuntimeArtifact)
	a, _ := json.Marshal(artifact)
	c, _ := json.Marshal(src.Config)
	return aws.ToString(live.AgentRuntimeVersion) + "\x00" + string(a) + "\x00" + string(c)
}

func (r *AgentCore) stillWaiting(arn, key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.waiting[arn]
	return entry.hint, ok && entry.key == key
}

func (r *AgentCore) rememberWaiting(arn, key, hint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hint == "" {
		delete(r.waiting, arn)
		return
	}
	if r.waiting == nil {
		r.waiting = map[string]agentCoreWaitingEntry{}
	}
	r.waiting[arn] = agentCoreWaitingEntry{key: key, hint: hint}
}

func (r *AgentCore) process(ctx context.Context, record agentcore.Runtime, src basestore.SourceRecord, want, observe bool, target dest.OTLPTarget, headers map[string]string, budget *agentCoreSlot) error {
	client, err := r.client(ctx, record.Region)
	if err != nil {
		return err
	}
	live, err := client.GetAgentRuntime(ctx, &ac.GetAgentRuntimeInput{AgentRuntimeId: aws.String(record.ID)})
	if err != nil {
		return err
	}
	endpoints, err := agentcore.Endpoints(ctx, client, record.ID)
	if err != nil {
		return err
	}
	current := agentcore.Describe(record.Region, live, endpoints)
	if err := r.store.UpsertAgentCore(ctx, current); err != nil {
		return err
	}
	if current.Status == "CREATING" || current.Status == "UPDATING" {
		state := map[string]any{"settling": true, "observation_error": ""}
		if !observe && current.Origin != "" {
			state["instrumentation_phase"] = baseapi.PhaseInstrumenting
		}
		return r.setState(ctx, record.ARN, state)
	}
	if current.Status != "READY" {
		return fmt.Errorf("runtime is %s; wait for a successful application deployment", current.Status)
	}
	if observe && !want {
		return nil
	} // discovery refreshes attribution; disabled IaC is never removed.
	if !want {
		if live.EnvironmentVariables["ODIGOS_MANAGED"] == "" {
			return nil
		}
		return r.rollback(ctx, client, live, budget)
	}
	var installation *agentCoreInstallation
	var owned *agentCoreSnapshot
	if raw, present := live.EnvironmentVariables["ODIGOS_MANAGED"]; present {
		marker, err := parseAgentCoreMarker(raw)
		if err != nil {
			return err
		}
		snapshot, err := r.loadSnapshot(ctx, current.ARN, marker.Snapshot)
		if err != nil {
			return err
		}
		owned = &snapshot
	}
	actualArtifact, err := agentCoreArtifact(live.AgentRuntimeArtifact)
	if err != nil {
		return err
	}
	// Every installation records the artifact it left the runtime on, so any redeploy is detected again. A snapshot
	// that recorded none (from before artifacts were recorded) is detected once to record it.
	artifactChanged := owned != nil && (owned.InstalledArtifact == nil || !actualArtifact.equal(*owned.InstalledArtifact))
	// The artifact the customer deployed.
	originalArtifact := actualArtifact
	waitingKey := agentCoreWaitingKey(live, src)
	if !observe && (owned == nil || artifactChanged) {
		if current.Instances || current.Protocol != "" && current.Protocol != "HTTP" {
			return fmt.Errorf("%w: automatic artifact installation is qualified for HTTP microVM runtimes", ErrUnsupportedInstrumentation)
		}
		if live.EnvironmentVariables["ODIGOS_IAC"] != "" {
			return fmt.Errorf("customer-owned IaC instrumentation must be removed by its deployment owner before automatic installation")
		}
		if r.installer == nil {
			return fmt.Errorf("AgentCore automatic artifact installer is unavailable")
		}
		if !budget.take() {
			return nil
		}
		if hint, ok := r.stillWaiting(current.ARN, waitingKey); ok && owned == nil {
			return r.setWaiting(ctx, current.ARN, hint, false)
		}
		input := *live
		input.EnvironmentVariables = maps.Clone(live.EnvironmentVariables)
		if owned != nil {
			input.EnvironmentVariables, err = restoreAgentCoreEnv(input.EnvironmentVariables, *owned)
			if err != nil {
				return err
			}
			// The restored environment no longer names the agent's script: detect against the customer's entry
			// point when a redeploy kept the one Odigos set.
			// It is also what rollback returns to.
			if customer := owned.customerEntryPoint(actualArtifact); artifactChanged && customer != nil {
				input.AgentRuntimeArtifact = customer.sdk()
				originalArtifact = *customer
			}
		}
		// Written directly rather than through setState: the detection may well end in the same wait, which keeps
		// being reported while it runs.
		_ = r.store.SetAgentCoreState(ctx, current.ARN, map[string]any{"instrumentation_phase": baseapi.PhaseInstrumenting, "instrumentation_error": ""})
		detecting := time.Now()
		agentCoreLog.Info("agentcore detecting artifact", "runtime", current.Name, "version", current.Version, "redeployed", artifactChanged)
		result, waiting, err := r.installer.Resolve(ctx, &input, src)
		if err != nil {
			return err
		}
		if waiting != nil {
			agentCoreLog.Info("agentcore runtime waiting for its artifact", "runtime", current.Name, "hint", waiting.Hint,
				"duration", time.Since(detecting).String())
			return r.wait(ctx, client, live, owned, *waiting, waitingKey, budget)
		}
		agentCoreLog.Info("agentcore artifact detected", "runtime", current.Name, "sdk", result.SDK,
			"distros", result.Package.Distros, "duration", time.Since(detecting).String())
		r.rememberWaiting(current.ARN, "", "")
		installation = &result
		current.Package = result.Package
		current.Language = result.Package.Language
	}
	language := current.Language
	if override := src.Config.RuntimeOverride.Language; override != "" {
		if current.Language != "" && current.Language != override {
			return fmt.Errorf("%w: runtime override does not match the deployed AgentCore package", ErrUnsupportedRuntime)
		}
		language = override
	}
	distroName := src.Config.DistroOverride.OtelDistroName
	if distroName == "" && !observe && len(current.Package.Distros) == 1 && current.Package.Language == language {
		// The artifact carries one SDK (one package extra, or ADOT): that is the selection, whatever the default.
		distroName = current.Package.Distros[0]
	}
	distro, err := ResolveAgentCoreDistro(language, distroName)
	if err != nil {
		return err
	}
	// Automatic mode knows what the artifact carries: a selection it cannot start is the same wait as a missing
	// SDK. Manual mode reports the customer's own preparation instead.
	if !observe && current.Package.Version == 1 && current.Package.Language == language {
		if w := agentCoreDistroWaiting(distro.Name, current.Package.Distros); w != nil {
			return r.wait(ctx, client, live, owned, *w, waitingKey, budget)
		}
	}
	if err := agentCoreEligible(current, distro); err != nil {
		return err
	}
	service := src.ReportedServiceName()
	if service == "" {
		service = current.Name
	}
	sdk := agentCorePackageSDK(current.Package)
	if installation != nil {
		sdk = installation.SDK
	} else if owned != nil && (owned.SDK != "" || owned.Mode != "") {
		sdk = agentCoreSnapshotSDK(*owned)
	}
	desired := agentCoreRecipe(sdk, distro, service, target, headers)
	if installation != nil {
		maps.Copy(desired, installation.LoaderEnv)
		marker, _ := json.Marshal(installation.Package)
		desired[agentcore.PackageEnv] = string(marker)
	} else if owned != nil {
		for _, key := range []string{agentcore.PackageEnv, agentcore.EntrypointEnv, "PYTHONPATH", "NODE_OPTIONS"} {
			if value, ok := owned.Applied[key]; ok {
				desired[key] = value
			}
		}
	}
	if observe {
		// A self-managed runtime's own configuration must also start the SDK its contract declares.
		return r.observe(ctx, current, live.EnvironmentVariables, desired, agentCoreManualStartsSDK(live, sdk))
	}
	if live.EnvironmentVariables["ODIGOS_MANAGED"] == "" && live.EnvironmentVariables["ODIGOS_IAC"] != "" {
		return fmt.Errorf("customer-owned IaC instrumentation is present; remove it through its deployment owner before automatic management")
	}
	if installation == nil && live.EnvironmentVariables["ODIGOS_MANAGED"] != "" && len(agentCoreDiff(live.EnvironmentVariables, desired)) == 0 {
		marker, err := parseAgentCoreMarker(live.EnvironmentVariables["ODIGOS_MANAGED"])
		if err != nil {
			return err
		}
		if marker.Distro != distro.Name {
			return fmt.Errorf("AgentCore ownership marker does not match the selected distro")
		}
		if _, err := r.loadSnapshot(ctx, current.ARN, marker.Snapshot); err != nil {
			return err
		}
		return r.observe(ctx, current, live.EnvironmentVariables, desired, true)
	}
	if !budget.take() {
		return nil
	}
	next, err := r.managedEnv(ctx, current.ARN, live.EnvironmentVariables, desired, distro.Name, installation, originalArtifact)
	if err != nil {
		return err
	}
	if err := validateAgentCoreEnv(next); err != nil {
		return err
	}
	// Images are left as deployed; only a changed ZIP entry point is written.
	var artifact *agentCoreArtifactRef
	if installation != nil && !installation.Artifact.equal(actualArtifact) {
		artifact = &installation.Artifact
	}
	if err := r.update(ctx, client, live, next, artifact); err != nil {
		return err
	}
	budget.spend()
	agentCoreLog.Info("agentcore runtime updated", "runtime", current.Name, "sdk", sdk, "distro", distro.Name, "entry_point_changed", artifact != nil)
	if err := r.setState(ctx, current.ARN, map[string]any{"instrumentation_error": "", "instrumentation_phase": baseapi.PhaseInstrumenting, "settling": true}); err != nil {
		return err
	}
	return r.confirm(ctx, client, record, src, target, headers)
}

// confirm waits for DEFAULT to serve the version an update just created, then observes the runtime so it reads as
// instrumented in this pass rather than the next. The observation runs with no budget: it can report, never change.
// A runtime that does not settle in time keeps settling, and the next pass observes it.
func (r *AgentCore) confirm(ctx context.Context, client agentcore.Client, record agentcore.Runtime, src basestore.SourceRecord,
	target dest.OTLPTarget, headers map[string]string) error {
	if r.confirmFor <= 0 {
		return nil
	}
	started := time.Now()
	for {
		if live, err := client.GetAgentRuntime(ctx, &ac.GetAgentRuntimeInput{AgentRuntimeId: aws.String(record.ID)}); err == nil && live.Status == "READY" {
			if endpoints, err := agentcore.Endpoints(ctx, client, record.ID); err == nil && agentCoreDefaultServes(endpoints, aws.ToString(live.AgentRuntimeVersion)) {
				agentCoreLog.Info("agentcore DEFAULT serves the update", "runtime", record.Name, "version", aws.ToString(live.AgentRuntimeVersion),
					"after", time.Since(started).String())
				return r.process(ctx, record, src, true, false, target, headers, &agentCoreSlot{})
			}
		}
		if time.Since(started)+r.confirmEvery > r.confirmFor {
			agentCoreLog.Info("agentcore DEFAULT not settled yet; the next pass confirms it", "runtime", record.Name, "waited", time.Since(started).String())
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(r.confirmEvery):
		}
	}
}

// agentCoreDefaultServes reports whether DEFAULT is ready on version, the condition observe needs to stop settling.
func agentCoreDefaultServes(endpoints []agentcore.Endpoint, version string) bool {
	for _, e := range endpoints {
		if e.Name == "DEFAULT" {
			return e.Status == "READY" && e.LiveVersion == version && (e.TargetVersion == "" || e.TargetVersion == version)
		}
	}
	return false
}

// wait reports a runtime whose artifact carries no SDK. A runtime Odigos still manages (a redeploy dropped the
// SDK) is first returned to the customer's configuration: its environment would otherwise keep configuring an
// SDK that is no longer there.
func (r *AgentCore) wait(ctx context.Context, client agentcore.Client, live *ac.GetAgentRuntimeOutput, owned *agentCoreSnapshot, w agentCoreWaiting, key string, budget *agentCoreSlot) error {
	arn := aws.ToString(live.AgentRuntimeArn)
	if owned == nil {
		r.rememberWaiting(arn, key, w.Hint)
		return r.setWaiting(ctx, arn, w.Hint, false)
	}
	if !budget.take() {
		return nil
	}
	if err := r.restore(ctx, client, live, *owned); err != nil {
		return err
	}
	budget.spend()
	return r.setWaiting(ctx, arn, w.Hint, true)
}

func (r *AgentCore) managedEnv(ctx context.Context, arn string, current, desired map[string]string, distro string, installation *agentCoreInstallation, original agentCoreArtifactRef) (map[string]string, error) {
	snapshot := agentCoreSnapshot{Previous: map[string]*string{}, Applied: maps.Clone(desired)}
	retired := map[string]*string{}
	if raw, owned := current["ODIGOS_MANAGED"]; owned {
		m, err := parseAgentCoreMarker(raw)
		if err != nil {
			return nil, err
		}
		old, err := r.loadSnapshot(ctx, arn, m.Snapshot)
		if err != nil {
			return nil, err
		}
		snapshot.Previous = old.Previous
		snapshot.OriginalArtifact, snapshot.InstalledArtifact, snapshot.Mode, snapshot.SDK = old.OriginalArtifact, old.InstalledArtifact, old.Mode, old.SDK
		for key := range old.Applied {
			if _, kept := desired[key]; !kept {
				retired[key] = old.Previous[key]
			}
		}
	}
	// Existing OTEL_* and AGENT_OBSERVABILITY_ENABLED values are configuration for the one SDK the artifact
	// starts, not a second SDK: they are overridden and recorded in Previous like any managed key, and rollback
	// restores them.
	if installation != nil {
		snapshot.Mode, snapshot.SDK, snapshot.OriginalArtifact, snapshot.InstalledArtifact = "", installation.SDK, &original, &installation.Artifact
	}
	for key := range desired {
		if _, known := snapshot.Previous[key]; !known {
			if old, ok := current[key]; ok {
				v := old
				snapshot.Previous[key] = &v
			} else {
				snapshot.Previous[key] = nil
			}
		}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	id := agentCoreHash(data)
	if err := r.store.SaveAgentCoreSnapshot(ctx, arn, id, data); err != nil {
		return nil, err
	}
	next := maps.Clone(current)
	if next == nil {
		next = map[string]string{}
	}
	for key, previous := range retired {
		if previous == nil {
			delete(next, key)
		} else {
			next[key] = *previous
		}
	}
	for key, value := range desired {
		if value == "" {
			delete(next, key)
		} else {
			next[key] = value
		}
	}
	marker, _ := json.Marshal(agentCoreMarker{Version: 1, Snapshot: id, Distro: distro})
	next["ODIGOS_MANAGED"] = string(marker)
	return next, nil
}

func (r *AgentCore) loadSnapshot(ctx context.Context, arn, id string) (agentCoreSnapshot, error) {
	var s agentCoreSnapshot
	raw, err := r.store.LoadAgentCoreSnapshot(ctx, arn, id)
	if err != nil {
		return s, fmt.Errorf("load private AgentCore rollback snapshot: %w", err)
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if s.Previous == nil || s.Applied == nil {
		return s, fmt.Errorf("invalid AgentCore rollback snapshot")
	}
	// PostgreSQL JSONB normalizes whitespace and key order; hash canonical typed JSON.
	canonical, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	if agentCoreHash(canonical) != id {
		return s, fmt.Errorf("AgentCore rollback snapshot digest mismatch")
	}
	return s, nil
}
func (r *AgentCore) rollback(ctx context.Context, client agentcore.Client, live *ac.GetAgentRuntimeOutput, budget *agentCoreSlot) error {
	m, err := parseAgentCoreMarker(live.EnvironmentVariables["ODIGOS_MANAGED"])
	if err != nil {
		return err
	}
	snapshot, err := r.loadSnapshot(ctx, aws.ToString(live.AgentRuntimeArn), m.Snapshot)
	if err != nil {
		return err
	}
	if _, err := restoreAgentCoreEnv(live.EnvironmentVariables, snapshot); err != nil {
		return err
	}
	if !budget.take() {
		return nil
	}
	if err := r.restore(ctx, client, live, snapshot); err != nil {
		return err
	}
	agentCoreLog.Info("agentcore runtime restored to its own configuration", "runtime", aws.ToString(live.AgentRuntimeName))
	budget.spend()
	return r.setState(ctx, aws.ToString(live.AgentRuntimeArn), map[string]any{"instrumentation_error": "", "instrumentation_phase": baseapi.PhaseInstrumenting, "settling": true})
}

// restore returns a runtime to the configuration the snapshot recorded. The environment is always restored. The
// artifact is restored only while the runtime still references the one Odigos left it on: a newer customer
// deployment keeps its own artifact and entry point.
func (r *AgentCore) restore(ctx context.Context, client agentcore.Client, live *ac.GetAgentRuntimeOutput, snapshot agentCoreSnapshot) error {
	next, err := restoreAgentCoreEnv(live.EnvironmentVariables, snapshot)
	if err != nil {
		return err
	}
	actual, err := agentCoreArtifact(live.AgentRuntimeArtifact)
	if err != nil {
		return err
	}
	ours := snapshot.InstalledArtifact != nil && actual.equal(*snapshot.InstalledArtifact)
	var artifact *agentCoreArtifactRef
	if ours && snapshot.OriginalArtifact != nil && !snapshot.OriginalArtifact.equal(actual) {
		artifact = snapshot.OriginalArtifact
	} else if !ours {
		// A redeploy kept the entry point Odigos set: the wrapper alone cannot start the agent once the restored
		// environment drops its script, so the customer's entry point comes back with the new code.
		artifact = snapshot.customerEntryPoint(actual)
	}
	return r.update(ctx, client, live, next, artifact)
}
func agentCoreHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (r *AgentCore) update(ctx context.Context, client agentcore.Client, live *ac.GetAgentRuntimeOutput, env map[string]string, artifact *agentCoreArtifactRef) error {
	if err := validateAgentCoreRuntimeEnv(live, env); err != nil {
		return err
	}
	// AgentCore has no conditional revision parameter. Re-read immediately before writing to
	// avoid knowingly overwriting a concurrent deployment; a residual service-side race remains.
	fresh, err := client.GetAgentRuntime(ctx, &ac.GetAgentRuntimeInput{AgentRuntimeId: live.AgentRuntimeId})
	if err != nil {
		return err
	}
	if fresh.Status != "READY" || aws.ToString(fresh.AgentRuntimeVersion) != aws.ToString(live.AgentRuntimeVersion) {
		return fmt.Errorf("AgentCore configuration changed during reconciliation; retry after the deployment settles")
	}
	// Copy every deployment setting. Updating only environment would reset unrelated configuration.
	in := &ac.UpdateAgentRuntimeInput{AgentRuntimeId: live.AgentRuntimeId, AgentRuntimeArtifact: live.AgentRuntimeArtifact, RoleArn: live.RoleArn,
		AuthorizerConfiguration: live.AuthorizerConfiguration, CapacityProviderConfiguration: live.CapacityProviderConfiguration,
		Description: live.Description, EnvironmentVariables: env, FilesystemConfigurations: live.FilesystemConfigurations,
		LifecycleConfiguration: live.LifecycleConfiguration, MetadataConfiguration: live.MetadataConfiguration,
		NetworkConfiguration: live.NetworkConfiguration, PlatformVersion: live.PlatformVersion,
		ProtocolConfiguration: live.ProtocolConfiguration, RequestHeaderConfiguration: live.RequestHeaderConfiguration}
	if artifact != nil {
		in.AgentRuntimeArtifact = artifact.sdk()
	}
	if in.AgentRuntimeArtifact == nil || aws.ToString(in.RoleArn) == "" {
		return fmt.Errorf("runtime artifact or execution role missing; refusing update")
	}
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	in.ClientToken = aws.String(agentCoreHash(append([]byte(aws.ToString(live.AgentRuntimeVersion)+":"), data...)))
	out, err := client.UpdateAgentRuntime(ctx, in)
	if err != nil {
		return err
	}
	// Persist accepted ownership immediately so disabling a source before the next discovery
	// cycle still rolls it back. The next live read confirms readiness and endpoint coverage.
	accepted := *live
	accepted.EnvironmentVariables = env
	accepted.AgentRuntimeArtifact = in.AgentRuntimeArtifact
	accepted.AgentRuntimeVersion = out.AgentRuntimeVersion
	accepted.Status = out.Status
	if accepted.Status == "" {
		accepted.Status = "UPDATING"
	}
	parts := strings.Split(aws.ToString(live.AgentRuntimeArn), ":")
	if len(parts) < 6 {
		return fmt.Errorf("invalid AgentCore runtime ARN after update")
	}
	return r.store.UpsertAgentCore(ctx, agentcore.Describe(parts[3], &accepted, nil))
}

func (r *AgentCore) observe(ctx context.Context, current agentcore.Runtime, env, desired map[string]string, startsSDK bool) error {
	verdict := baseapi.ConformanceConformant
	var missing []string
	if current.Origin == "" {
		verdict = baseapi.ConformanceAbsent
	} else {
		if len(agentCoreDiff(env, desired)) > 0 {
			verdict = baseapi.ConformancePartial
			missing = append(missing, baseapi.ComponentCollectorExport)
		}
		if !startsSDK {
			verdict = baseapi.ConformancePartial
			missing = append(missing, baseapi.ComponentAgentLoader)
		}
	}
	settling := false
	defaultFound := false
	var pinned []string
	for _, e := range current.Endpoints {
		if e.Name == "DEFAULT" {
			defaultFound = true
			if e.LiveVersion != current.Version || (e.TargetVersion != "" && e.TargetVersion != current.Version) || e.Status != "READY" {
				settling = true
			}
		}
		if e.Name != "DEFAULT" && e.LiveVersion != current.Version {
			pinned = append(pinned, e.Name+" (version "+e.LiveVersion+")")
		}
	}
	if !defaultFound {
		settling = true
		verdict = baseapi.ConformancePartial
		missing = append(missing, baseapi.ComponentAgentLoader)
	}
	previous, err := r.store.AgentCoreState(ctx, current.ARN)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	since := now
	if fmt.Sprint(previous["conformance"]) == string(verdict) {
		if old, ok := previous["conformance_since"].(string); ok && old != "" {
			since = old
		}
	}
	phase := ""
	if current.Origin != "" {
		phase = baseapi.PhaseInstrumented
		if settling {
			phase = baseapi.PhaseInstrumenting
		}
	}
	var note *baseapi.WorkloadRoutingNote
	if len(pinned) > 0 {
		note = &baseapi.WorkloadRoutingNote{Code: "agentcore-endpoint-coverage", Message: "Automatic instrumentation covers DEFAULT. Custom endpoints still reference other versions: " + strings.Join(pinned, ", ") + ". Deploy instrumentation through their release owner. Existing sessions may also retain older code."}
	}
	return r.setState(ctx, current.ARN, map[string]any{"conformance": verdict, "conformance_missing": missing, "conformance_since": since,
		"observation_error": "", "instrumentation_error": "", "instrumentation_phase": phase, "settling": settling, "routing_note": note})
}
