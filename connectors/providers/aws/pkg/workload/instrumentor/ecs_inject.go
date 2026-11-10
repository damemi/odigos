package instrumentor

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// OTLP export + identity env the ECS instrumentor sets on the app container. Unlike Lambda (which
// exports to an in-Lambda collector on localhost), a Fargate task has no node-local collector, so the
// SDK/agent exports OTLP to its resolved global or regional target. OTEL_EXPORTER_OTLP_ENDPOINT is the
// same var Lambda strips as legacy (envLegacyOTLPEndpoint) — here it is the primary export target.
const (
	envOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
	envResourceAttributes = "OTEL_RESOURCE_ATTRIBUTES"
)

// Sidecar + shared-volume wiring: the ECS-native adaptation of the OSS Odigos "k8s-init-container" mount
// method (and of the same design on GCP Cloud Run). ECS supports real run-to-completion containers, so —
// unlike Cloud Run's startup-probe/port-holding workaround — the agent copier is simply a non-essential
// container that stages the files and exits 0, and the app container DependsOn it with condition SUCCESS
// (which is only valid against non-essential containers). The shared volume is a bare-name task volume
// (a Fargate ephemeral-storage bind mount; Fargate has no tmpfs) mounted at /var/odigos in both.
const (
	sidecarContainerName = "odigos-agent"
	agentVolumeName      = "odigos-agent"
	// maxTaskDefContainers is AWS's limit on containers per task definition; adding the sidecar to a task
	// definition already at the limit would be rejected by RegisterTaskDefinition.
	maxTaskDefContainers = 10
)

// ecsExportTarget is the per-language OTLP export wiring: the endpoint URL and protocol the injected
// agent exports with (gRPC for java/python/nodejs/dotnet, http/protobuf for php/ruby), plus the signals
// the destination accepts.
type ecsExportTarget struct {
	endpoint string // scheme://host:port
	protocol string // "grpc" | "http/protobuf"
	headers  map[string]string
	traces   bool
	metrics  bool
	logs     bool
}

func (t ecsExportTarget) signals() domaindest.OTLPTarget {
	return domaindest.OTLPTarget{Traces: t.traces, Metrics: t.metrics, Logs: t.logs}
}

// containerTarget is one container the reconcile instruments, with all per-container wiring already
// resolved by the caller.
//
// A task definition can pack several independently instrumentable applications — six apps in six
// languages is a real customer shape — so instrumentation is expressed as a SET of targets rather
// than one chosen container. Each target carries its own recipe (its language's agent and activation
// env) and its own export target (php/ruby can only speak http/protobuf, java can speak either), so
// containers in one task may legitimately differ in both.
type containerTarget struct {
	idx     int
	name    string
	runtime resolvedRuntime
	recipe  agentRecipe
	export  ecsExportTarget
}

// applyInstrumentation mutates td into the desired instrumented state for every target — configuring
// each target container (agent activation + OTLP export env + ownership marker + shared-volume mount
// + startup dependency) and adding the run-to-completion agent sidecar and shared volume — and
// returns the RegisterTaskDefinitionInput for the new revision. It is idempotent: re-applying the
// same desired state yields the same task definition.
//
// Targets are chosen by the caller (see candidateContainerIndexes plus the instrumentor's
// runtime-based detection), because selection consults language detection, which needs I/O this pure
// mutation layer must not do.
func applyInstrumentation(td *ecstypes.TaskDefinition, targets []containerTarget, tags []ecstypes.Tag, image, serviceName, region string, debug bool) (*ecs.RegisterTaskDefinitionInput, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no container targets for task definition %s", aws.ToString(td.Family))
	}
	for _, t := range targets {
		if t.idx < 0 || t.idx >= len(td.ContainerDefinitions) {
			return nil, fmt.Errorf("container index %d out of range for task definition %s", t.idx, aws.ToString(td.Family))
		}
		if err := checkSecretsCollisions(&td.ContainerDefinitions[t.idx], t.recipe); err != nil {
			return nil, err
		}
	}
	if findECSContainer(td, sidecarContainerName) == nil && len(td.ContainerDefinitions) >= maxTaskDefContainers {
		return nil, fmt.Errorf("%w: task definition %s already has %d containers — the AWS maximum — leaving no room for the odigos-agent sidecar",
			ErrUnsupportedInstrumentation, aws.ToString(td.Family), len(td.ContainerDefinitions))
	}

	// A container we instrumented before but that is no longer a target (its override disabled it, its
	// runtime stopped resolving, the customer changed its image) must be restored in this same
	// revision. Leaving it carrying our env would strand an agent no sidecar stages any more.
	targetIdx := make(map[int]bool, len(targets))
	for _, t := range targets {
		targetIdx[t.idx] = true
	}
	for _, idx := range managedContainerIndexes(td) {
		if !targetIdx[idx] {
			rollbackContainer(&td.ContainerDefinitions[idx])
		}
	}

	// Add the sidecar (an append that may reallocate ContainerDefinitions) BEFORE taking any container
	// pointer, so every mutation below lands in the slice the register input is built from. The
	// sidecar stages the union of every target's agent directories — one copier serves them all.
	ensureAgentVolume(td)
	ensureSidecar(td, image, unionAgentDirs(targets))

	for _, t := range targets {
		if err := instrumentContainer(&td.ContainerDefinitions[t.idx], t, serviceName, region, debug); err != nil {
			return nil, err
		}
	}
	return registerInputFromTaskDefinition(td, tags), nil
}

// instrumentContainer applies one target's wiring to its container.
func instrumentContainer(app *ecstypes.ContainerDefinition, t containerTarget, serviceName, region string, debug bool) error {
	env := containerEnvMap(app)
	for _, key := range domaindest.SignalTransportOverrides() {
		if t.runtime.imageEnv[key] != "" {
			return fmt.Errorf("%w: container %s defines %s in its image; remove image-level per-signal transport overrides before instrumentation", ErrUnsupportedInstrumentation, t.name, key)
		}
	}
	if len(app.EnvironmentFiles) > 0 {
		return fmt.Errorf("%w: container %s uses environment files whose loader and per-signal settings cannot be verified; inline the environment in the task definition and remove environmentFiles before instrumentation", ErrUnsupportedInstrumentation, t.name)
	}

	manifest := buildECSManifest(env, env[store.EnvOdigosManaged], t.recipe)
	if manifest.LoaderEnv == nil {
		manifest.LoaderEnv = map[string]string{}
	}

	for k, v := range t.recipe.staticEnv {
		setContainerEnv(app, k, v)
	}
	for _, a := range t.recipe.appendEnv {
		// Rebuild from the customer's ORIGINAL value (snapshot), not the current one, so a re-instrument
		// never accumulates our value.
		original := ecsPriorEnv(manifest, a.name)
		if manifest.PrevEnv[a.name] == nil {
			if inherited, known := t.runtime.imageEnv[a.name]; known {
				original = inherited
			} else {
				original = manifest.LoaderEnv[a.name]
			}
			manifest.LoaderEnv[a.name] = original
		}
		setContainerEnv(app, a.name, a.evaluate(original))
	}

	setContainerEnv(app, EnvServiceName, serviceName)
	setContainerEnv(app, envResourceAttributes, ecsResourceAttributes(region))
	setContainerEnv(app, envOTLPEndpoint, t.export.endpoint)
	if headers := otlpHeadersEnv(t.export.headers); headers != "" {
		setContainerEnv(app, envOTLPHeaders, headers)
	} else {
		deleteContainerEnv(app, envOTLPHeaders)
	}
	setContainerEnv(app, EnvOtlpProtocol, t.export.protocol)
	for key, value := range t.export.signals().SignalActivation(t.recipe.lang) {
		setContainerEnv(app, key, value)
	}
	for _, key := range domaindest.SignalTransportOverrides() {
		deleteContainerEnv(app, key)
	}
	if debug {
		setContainerEnv(app, EnvOtelLogLevel, otelLogLevelDebug)
	} else {
		deleteContainerEnv(app, EnvOtelLogLevel)
	}

	marker, err := marshalECSManifest(manifest)
	if err != nil {
		return err
	}
	setContainerEnv(app, store.EnvOdigosManaged, marker)

	ensureAppMount(app)
	ensureDependsOn(app, sidecarContainerName)
	return nil
}

// unionAgentDirs is every agent directory the targets need, deduplicated and sorted so the sidecar
// command is byte-stable across reconciles (an unstable command would break convergence and churn a
// new revision every cycle). Directories are per language, and the whole language directory is
// staged — the runtime version is templated into the activation env, not into what is copied — so
// two php containers on different versions share one entry.
func unionAgentDirs(targets []containerTarget) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, t := range targets {
		for _, d := range t.recipe.agentDirs {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	sort.Strings(dirs)
	return dirs
}

// Component names for the conformance diffs below. They are the provider-neutral vocabulary the BASE renders
// into customer-facing prose (base/domain/workloadreport.componentLabels), shared with Lambda wherever the
// concept is the same: a stale service name or export setting reads identically on either platform, so it must
// carry the same name. The sidecar/volume/dependency triple is specific to the staged-agent platforms.
//
// A name added here needs a label there, or the report falls through to printing the raw name — degraded, but
// never silent.
const (
	componentAgentSidecar           = baseapi.ComponentAgentSidecar
	componentAgentVolume            = baseapi.ComponentAgentVolume
	componentAgentVolumeMount       = baseapi.ComponentAgentVolumeMount
	componentAgentStartupDependency = baseapi.ComponentAgentStartupDependency
	componentAgentLanguage          = baseapi.ComponentAgentLanguage
	componentResourceAttributes     = baseapi.ComponentResourceAttributes
)

// alreadyInstrumented reports whether td already carries the exact desired instrumentation for the
// recipe, so the reconcile can skip RegisterTaskDefinition+UpdateService entirely (steady state = zero
// AWS writes). It checks the managed state — never task-definition-ARN equality, since a customer deploy
// registers a fresh unmarked revision that must trigger a re-instrument.
func alreadyInstrumented(td *ecstypes.TaskDefinition, targets []containerTarget, image, serviceName, region string, debug bool) bool {
	if len(targets) == 0 {
		return false
	}
	// The instrumented set must match the target set EXACTLY. A container we own but no longer target
	// has to be rolled back, and a target we do not yet own has to be instrumented — either way the
	// task definition is not converged, so this cannot be a per-target check alone.
	//
	// This set check is deliberately NOT part of the diff functions below. For the mutating path a mismatch
	// means "converge it", which is free; for the read-only observer it would mean telling a customer their
	// deployment has drifted because Odigos detected a container they did not instrument — a coverage
	// opinion, not decay, and one that rests on language detection the manual-mode policy does not fully
	// grant. See ecsObservedDiff.
	owned := managedContainerIndexes(td)
	if len(owned) != len(targets) {
		return false
	}
	ownedIdx := make(map[int]bool, len(owned))
	for _, idx := range owned {
		ownedIdx[idx] = true
	}
	for _, t := range targets {
		if !ownedIdx[t.idx] {
			return false
		}
		if len(containerDiff(&td.ContainerDefinitions[t.idx], t, serviceName, region, debug)) > 0 {
			return false
		}
	}
	return len(taskLevelDiff(td, targets, connectorAgentsImage(image))) == 0
}

// agentsImageTarget is which agents bundle a task definition's staging container is HELD TO, and whether it
// is held to one at all.
//
// The distinction exists because the answer has two different sources. When the connector owns the task
// definition it wrote the staging container itself and converges it to its own configured image, so the
// image is pinned and exact. When a customer's IaC applied the instrumentation, the image they staged from
// is whatever their artifact named — and holding them to THIS connector build's pin instead is what turns
// an ordinary artifacts-version bump into drift reported against every manually instrumented task in the
// account, all at once, for a change nobody made to their deployment.
type agentsImageTarget struct {
	// image is the reference the staging container must carry. Empty when nothing recorded one.
	image string
	// pinned is false when no image was recorded, in which case the staging container is checked by
	// SIGNATURE (present, non-essential, staging the right agents) and not by identity — the same fallback
	// the Lambda layer check makes for a function whose marker recorded no layer ARN.
	pinned bool
	// conflict is true when the instrumented containers recorded DIFFERENT images, which is a task
	// definition assembled from two releases of the artifacts and cannot be satisfied by one staging
	// container. Reported as drift rather than resolved by picking one.
	conflict bool
}

// connectorAgentsImage is the target for a task definition the connector owns: its own configured image,
// exactly. Recorded images play no part — see agentsImageTarget.
func connectorAgentsImage(image string) agentsImageTarget {
	return agentsImageTarget{image: image, pinned: true}
}

// desiredAgentsImage resolves which agents bundle the staging container is held to, from the markers the
// instrumented containers carry.
//
// One OWNERSHIP marker anywhere is decisive: the connector may mutate this task definition and converges it
// to its own pin, so a recorded image on some other container is not a version to honour but a leftover from
// before the takeover.
//
// Otherwise every container is the customer's own, and the recorded images are the only statement of what
// they meant to stage. They must agree — one staging container cannot serve two bundles — so more than one
// distinct value is a conflict, and none at all leaves the check unpinned.
func desiredAgentsImage(td *ecstypes.TaskDefinition, instrumentedIdx []int, connectorImage string) agentsImageTarget {
	recorded := map[string]bool{}
	for _, idx := range instrumentedIdx {
		m, ok := readECSMarker(containerEnvMap(&td.ContainerDefinitions[idx]))
		if !ok {
			continue
		}
		if m.Managed {
			return connectorAgentsImage(connectorImage)
		}
		if m.Image != "" {
			recorded[m.Image] = true
		}
	}
	switch len(recorded) {
	case 0:
		return agentsImageTarget{}
	case 1:
		for image := range recorded {
			return agentsImageTarget{image: image, pinned: true}
		}
	}
	return agentsImageTarget{conflict: true}
}

// taskLevelDiff reports what is missing from the task-level agent staging: the sidecar that copies the agent
// files into the shared volume, and the volume itself.
//
// These are the ECS analog of Lambda's detached layer, and the reason marker-presence checking is a false
// green here too: a container can carry a complete, correct set of activation env vars while the sidecar that
// puts the agent on disk is gone, in which case the application starts, finds nothing at /var/odigos, and
// exports no telemetry at all — while reporting as instrumented.
func taskLevelDiff(td *ecstypes.TaskDefinition, targets []containerTarget, want agentsImageTarget) []string {
	var diff []string
	if !hasAgentVolume(td) {
		diff = append(diff, componentAgentVolume)
	}
	sc := findECSContainer(td, sidecarContainerName)
	// Non-essentiality and the exact copy command always matter: an essential copier fails the whole task
	// when it exits, and a command missing a language's directory stages nothing for that container.
	stale := sc == nil ||
		sc.Essential == nil || *sc.Essential ||
		strings.Join(sc.Command, "\x00") != strings.Join(sidecarCommand(unionAgentDirs(targets)), "\x00")
	// The image is compared only when something states which one this task definition is supposed to carry.
	// A stale image stages the wrong agent build, so where there IS a pin the comparison is exact; where
	// there is not, demanding this build's pin would manufacture drift out of an Odigos release.
	if want.conflict || (want.pinned && sc != nil && aws.ToString(sc.Image) != want.image) {
		stale = true
	}
	if stale {
		diff = append(diff, componentAgentSidecar)
	}
	return diff
}

// containerDiff reports the components of one container's desired wiring that are absent or stale — empty
// when it already carries exactly its target's instrumentation.
//
// It replaces what used to be a bare bool (containerConverged), for the reason §7.2 gives: as a bool its only
// consumer was "may I skip this RegisterTaskDefinition", and the detail was worthless because the instrumentor
// was about to converge whatever was wrong. In manual mode nothing converges it — the customer edits their own
// task definition — so the component list is the only thing that tells them where to look. Same predicate,
// same call site for the mutating path (alreadyInstrumented above), one new consumer that needs the facts.
func containerDiff(app *ecstypes.ContainerDefinition, t containerTarget, serviceName, region string, debug bool) []string {
	env := containerEnvMap(app)
	marker, ok := readECSMarker(env)
	if !ok {
		// NEITHER marker: nothing on this container is attributable to Odigos, so there is nothing to compare
		// against. Reporting the individual components would describe an uninstrumented container — or a
		// customer's own OpenTelemetry setup — as broken Odigos instrumentation.
		return []string{componentOwnershipMarker}
	}
	var diff []string
	// Cross-check the recorded agent language against the resolved one (env alone can miss an image swap).
	// Missing language → skip: omission is not drift (only hand-authored markers omit it; published IaC includes it).
	if marker.Language != "" && marker.Language != t.recipe.lang {
		diff = append(diff, componentAgentLanguage)
	}
	if env[EnvServiceName] != serviceName {
		diff = append(diff, componentServiceName)
	}
	if env[envResourceAttributes] != ecsResourceAttributes(region) {
		diff = append(diff, componentResourceAttributes)
	}
	if env[envOTLPEndpoint] != t.export.endpoint ||
		env[envOTLPHeaders] != otlpHeadersEnv(t.export.headers) ||
		env[EnvOtlpProtocol] != t.export.protocol ||
		!t.export.signals().SignalConfigurationMatches(env, t.recipe.lang) {
		diff = append(diff, componentCollectorExport)
	}
	wantLogLevel := ""
	if debug {
		wantLogLevel = otelLogLevelDebug
	}
	if env[EnvOtelLogLevel] != wantLogLevel {
		diff = append(diff, componentLogLevel)
	}
	// The recipe's static and append-style env is what actually activates the agent; without it the files are
	// staged and never loaded.
	activated := true
	for k, v := range t.recipe.staticEnv {
		if env[k] != v {
			activated = false
			break
		}
	}
	if activated {
		for _, a := range t.recipe.appendEnv {
			if !loaderActivated(env[a.name], a, marker) {
				activated = false
				break
			}
		}
	}
	if !activated {
		diff = append(diff, componentAgentLoader)
	}
	if !containerHasMount(app) {
		diff = append(diff, componentAgentVolumeMount)
	}
	if !containerDependsOnSidecar(app) {
		diff = append(diff, componentAgentStartupDependency)
	}
	return diff
}

// otlpHeadersEnv renders the OpenTelemetry SDK header-list syntax deterministically. Values are URL encoded
// per the SDK specification so commas, equals signs and credentials cannot change the list's structure.
func otlpHeadersEnv(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(headers[key]))
	}
	return strings.Join(parts, ",")
}

// loaderActivated reports whether the container's loader variable loads our agent.
// Ownership marker: exact match — the connector owns the whole value from the snapshot.
// IaC marker: our fragment must be present on the required side; the rest of the value is the customer's
// and was never snapshotted (demanding an exact value would invent drift from a typo).
func loaderActivated(live string, a appendEnvVar, marker ecsMarker) bool {
	if marker.Managed {
		return live == a.evaluate(markerPriorEnv(marker, a.name))
	}
	ours := a.evaluate("")
	if ours == "" {
		return true
	}
	// A SUFFIX test, not a substring one: the pattern appends, so our fragment has to come last. A value that
	// merely contains it — prepended, or spliced into the middle of a PYTHONPATH — resolves a different copy of
	// the agent first, or none, and calling that conformant would tell a customer instrumentation works when it
	// does not.
	if sep := a.separator(); sep != "" && strings.HasSuffix(live, sep+ours) {
		return true
	}
	// The container carried nothing of its own, so our fragment IS the whole value.
	return live == ours
}

// rollbackInstrumentation restores td to its pre-Odigos state from the ownership marker and returns the
// RegisterTaskDefinitionInput for the clean revision. It returns (nil, false) when the task definition
// carries no Odigos ownership marker (never ours — left completely untouched).
func rollbackInstrumentation(td *ecstypes.TaskDefinition, tags []ecstypes.Tag) (*ecs.RegisterTaskDefinitionInput, bool) {
	owned := managedContainerIndexes(td)
	if len(owned) == 0 {
		return nil, false
	}
	restored := false
	for _, idx := range owned {
		if rollbackContainer(&td.ContainerDefinitions[idx]) {
			restored = true
		}
	}
	if !restored {
		return nil, false
	}
	// The sidecar and volume are shared by every instrumented container, so they go once all of them have
	// been restored — UNLESS a container the connector never owned is still relying on them.
	//
	// That is the takeover-then-release case, and stripping them there is destructive rather than merely
	// untidy: a container carrying the customer's own IaC marker keeps its activation environment through
	// this rollback (that environment IS what the snapshot restores), so removing the staging container
	// leaves it pointing at agent files nothing puts on disk — instrumented, reporting as instrumented, and
	// exporting nothing. Leaving them is recoverable and visible: the customer's own next deploy reasserts
	// their definition, and until then the observer judges the staging container against the containers that
	// still carry a marker, so a command that has become wrong is reported rather than hidden.
	if len(instrumentedContainerIndexes(td)) == 0 {
		removeECSContainer(td, sidecarContainerName)
		removeAgentVolume(td)
	}
	return registerInputFromTaskDefinition(td, tags), true
}

// rollbackContainer restores one container to its pre-Odigos state from its ownership marker,
// reporting whether it had a readable one. The shared sidecar and volume are the task's, not the
// container's, so they are the caller's to remove.
func rollbackContainer(app *ecstypes.ContainerDefinition) bool {
	env := containerEnvMap(app)
	manifest, ok := parseECSManifest(env[store.EnvOdigosManaged])
	if !ok {
		return false
	}
	for k, prev := range manifest.PrevEnv {
		if prev == nil {
			deleteContainerEnv(app, k)
		} else {
			setContainerEnv(app, k, *prev)
		}
	}
	deleteContainerEnv(app, store.EnvOdigosManaged)
	// The mount and the startup dependency are removed only if this container is not still instrumented by
	// the customer's own IaC. The snapshot cannot decide this on its own: ensureAppMount and ensureDependsOn
	// are idempotent, so a container that already had them when the connector took it over recorded no
	// difference to restore, and removing them unconditionally would delete wiring the connector never added.
	// The surviving IaC marker is the evidence that they are still needed.
	if _, stillInstrumented := readECSMarker(containerEnvMap(app)); !stillInstrumented {
		removeAppMount(app)
		removeDependsOn(app, sidecarContainerName)
	}
	return true
}

// registerInputFromTaskDefinition rebuilds a RegisterTaskDefinitionInput from a described task
// definition: registering a new revision requires round-tripping every revision-defining field (the
// output type and input type differ, so the copy is explicit). Output-only fields (arn, revision,
// status, registeredAt/by, requiresAttributes, compatibilities) are deliberately not copied. Keep this
// list in sync with the SDK — ecs_inject_test.go reflects over RegisterTaskDefinitionInput and fails
// when a new revision-defining field appears.
func registerInputFromTaskDefinition(td *ecstypes.TaskDefinition, tags []ecstypes.Tag) *ecs.RegisterTaskDefinitionInput {
	in := &ecs.RegisterTaskDefinitionInput{
		Family:                  td.Family,
		ContainerDefinitions:    td.ContainerDefinitions,
		Cpu:                     td.Cpu,
		Memory:                  td.Memory,
		EnableFaultInjection:    td.EnableFaultInjection,
		EphemeralStorage:        td.EphemeralStorage,
		ExecutionRoleArn:        td.ExecutionRoleArn,
		TaskRoleArn:             td.TaskRoleArn,
		InferenceAccelerators:   td.InferenceAccelerators,
		IpcMode:                 td.IpcMode,
		NetworkMode:             td.NetworkMode,
		PidMode:                 td.PidMode,
		PlacementConstraints:    td.PlacementConstraints,
		ProxyConfiguration:      td.ProxyConfiguration,
		RequiresCompatibilities: td.RequiresCompatibilities,
		RuntimePlatform:         td.RuntimePlatform,
		Volumes:                 td.Volumes,
	}
	if len(tags) > 0 {
		in.Tags = tags
	}
	return in
}

// --- app container selection ---

// managedContainerIndexes returns every container carrying our OWNERSHIP marker, ascending. This is the
// mutation predicate: only a container the connector instrumented itself may be re-instrumented or rolled
// back, because only its marker records what undoing the change means.
//
// It is deliberately narrower than instrumentedContainerIndexes. A container a customer instrumented in their
// own deployment definition carries the IaC marker and no snapshot, so treating it as ours would have the
// connector "restore" a container to values it never recorded — which in practice means deleting the
// customer's own JAVA_TOOL_OPTIONS.
func managedContainerIndexes(td *ecstypes.TaskDefinition) []int {
	var out []int
	for i := range td.ContainerDefinitions {
		if _, ok := containerEnvValue(&td.ContainerDefinitions[i], store.EnvOdigosManaged); ok {
			out = append(out, i)
		}
	}
	return out
}

// instrumentedContainerIndexes returns every container carrying EITHER marker, ascending. This is the
// read-only predicate — "does this container have Odigos instrumentation on it" — and it is what the observer
// and the report ask.
//
// Widening it here is what makes manual mode work at all: the customer applies the IaC marker, nothing ever
// writes the ownership marker, and a reader that recognized only the latter would report a fully and correctly
// instrumented task as awaiting a deployment that had already happened.
func instrumentedContainerIndexes(td *ecstypes.TaskDefinition) []int {
	var out []int
	for i := range td.ContainerDefinitions {
		if _, ok := readECSMarker(containerEnvMap(&td.ContainerDefinitions[i])); ok {
			out = append(out, i)
		}
	}
	return out
}

// containerManifestLanguage returns the language recorded in a container's marker, or "".
// It is the precise per-container memory of which agent was staged — more reliable than the
// workload-level stored runtime once a task holds several languages — and it comes from either marker,
// because in manual mode the customer's own definition is the only place that fact is written.
func containerManifestLanguage(c *ecstypes.ContainerDefinition) string {
	m, ok := readECSMarker(containerEnvMap(c))
	if !ok {
		return ""
	}
	return m.Language
}

// candidateContainerIndexes returns the containers eligible for instrumentation, as indexes into
// td.ContainerDefinitions (so callers can re-take pointers after slice-reallocating mutations): every
// essential container except our own sidecar, plus any container we already own.
//
// Owned containers are included even if they have since become non-essential, because dropping one
// silently would strand our env in it — a container we instrumented stays under our management until
// it is deliberately rolled back.
//
// Selection deliberately does NOT use port mappings. In the common proxy+app pattern (nginx in front
// of PHP-FPM, Envoy in front of a service) the port-mapped container is the PROXY, so picking it
// would instrument a container that has no agent while leaving the real application untouched.
func candidateContainerIndexes(td *ecstypes.TaskDefinition) ([]int, error) {
	var out []int
	for i := range td.ContainerDefinitions {
		c := &td.ContainerDefinitions[i]
		if aws.ToString(c.Name) == sidecarContainerName {
			continue
		}
		_, owned := containerEnvValue(c, store.EnvOdigosManaged)
		// Essential defaults to true when unset.
		if owned || c.Essential == nil || *c.Essential {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: task definition %s has no essential container to instrument", ErrUnsupportedInstrumentation, aws.ToString(td.Family))
	}
	return out, nil
}

// containerName returns a container's name for error messages ("<unnamed>" when absent).
func containerName(c *ecstypes.ContainerDefinition) string {
	if n := aws.ToString(c.Name); n != "" {
		return n
	}
	return "<unnamed>"
}

// checkSecretsCollisions rejects instrumentation when a managed env key is delivered via ECS `secrets`
// (valueFrom): RegisterTaskDefinition rejects a key present in both environment and secrets, and
// silently dropping the customer's secret would be worse. The customer resolves it by removing the
// secret-sourced key or setting a runtime override that avoids it.
func checkSecretsCollisions(app *ecstypes.ContainerDefinition, recipe agentRecipe) error {
	if len(app.Secrets) == 0 {
		return nil
	}
	secretNames := make(map[string]bool, len(app.Secrets))
	for _, s := range app.Secrets {
		secretNames[aws.ToString(s.Name)] = true
	}
	for _, k := range ecsManagedEnvKeys(recipe) {
		if secretNames[k] {
			return fmt.Errorf("%w: container %s sets %s via ECS secrets (valueFrom); Odigos must own that variable to instrument — remove the secret-sourced value to enable instrumentation",
				ErrUnsupportedInstrumentation, aws.ToString(app.Name), k)
		}
	}
	return nil
}

// --- env helpers over []ecstypes.KeyValuePair (order-preserving) ---

func containerEnvValue(c *ecstypes.ContainerDefinition, name string) (string, bool) {
	for _, e := range c.Environment {
		if aws.ToString(e.Name) == name {
			return aws.ToString(e.Value), true
		}
	}
	return "", false
}

func containerEnvMap(c *ecstypes.ContainerDefinition) map[string]string {
	out := make(map[string]string, len(c.Environment))
	for _, e := range c.Environment {
		out[aws.ToString(e.Name)] = aws.ToString(e.Value)
	}
	return out
}

// setContainerEnv sets an env var, replacing an existing entry in place (preserving order) or appending
// a new one.
func setContainerEnv(c *ecstypes.ContainerDefinition, name, value string) {
	for i := range c.Environment {
		if aws.ToString(c.Environment[i].Name) == name {
			c.Environment[i].Value = aws.String(value)
			return
		}
	}
	c.Environment = append(c.Environment, ecstypes.KeyValuePair{Name: aws.String(name), Value: aws.String(value)})
}

func deleteContainerEnv(c *ecstypes.ContainerDefinition, name string) {
	out := c.Environment[:0]
	for _, e := range c.Environment {
		if aws.ToString(e.Name) != name {
			out = append(out, e)
		}
	}
	c.Environment = out
}

// --- volume / mount / dependency / sidecar mutation ---

func ensureAgentVolume(td *ecstypes.TaskDefinition) {
	for _, v := range td.Volumes {
		if aws.ToString(v.Name) == agentVolumeName {
			return
		}
	}
	// A bare-name volume (no host/docker/EFS configuration) is a Fargate ephemeral-storage bind mount
	// scoped to the task — exactly the shared staging area the sidecar and app need.
	td.Volumes = append(td.Volumes, ecstypes.Volume{Name: aws.String(agentVolumeName)})
}

func ensureAppMount(c *ecstypes.ContainerDefinition) {
	for i := range c.MountPoints {
		if aws.ToString(c.MountPoints[i].SourceVolume) == agentVolumeName {
			c.MountPoints[i].ContainerPath = aws.String(agentsDir)
			c.MountPoints[i].ReadOnly = aws.Bool(true)
			return
		}
	}
	// Read-only in the app container: the agents are only read there; the sidecar owns the writes.
	c.MountPoints = append(c.MountPoints, ecstypes.MountPoint{
		SourceVolume:  aws.String(agentVolumeName),
		ContainerPath: aws.String(agentsDir),
		ReadOnly:      aws.Bool(true),
	})
}

func ensureDependsOn(c *ecstypes.ContainerDefinition, name string) {
	for _, d := range c.DependsOn {
		if aws.ToString(d.ContainerName) == name {
			return
		}
	}
	c.DependsOn = append(c.DependsOn, ecstypes.ContainerDependency{
		ContainerName: aws.String(name),
		Condition:     ecstypes.ContainerConditionSuccess,
	})
}

func ensureSidecar(td *ecstypes.TaskDefinition, image string, agentDirs []string) {
	desired := ecstypes.ContainerDefinition{
		Name:  aws.String(sidecarContainerName),
		Image: aws.String(image),
		// Non-essential: the copier exits 0 once staging is done; the app container's DependsOn SUCCESS
		// gates its start on that (SUCCESS may only reference a non-essential container).
		Essential: aws.Bool(false),
		Command:   sidecarCommand(agentDirs),
		MountPoints: []ecstypes.MountPoint{{
			SourceVolume:  aws.String(agentVolumeName),
			ContainerPath: aws.String(agentsDir),
		}},
	}
	for i := range td.ContainerDefinitions {
		if aws.ToString(td.ContainerDefinitions[i].Name) == sidecarContainerName {
			td.ContainerDefinitions[i] = desired
			return
		}
	}
	td.ContainerDefinitions = append(td.ContainerDefinitions, desired)
}

// sidecarCommand copies the language agent directories from the odigos-fargate-agents image into the
// shared volume, then exits 0 (run-to-completion; the agents image is busybox-based, so sh/cp are
// available). No port-holding is needed on ECS — the SUCCESS container dependency covers ordering.
func sidecarCommand(agentDirs []string) []string {
	var b strings.Builder
	b.WriteString("mkdir -p " + agentsDir)
	for _, d := range agentDirs {
		b.WriteString(" && cp -r " + imageAgentsDir + "/" + d + " " + agentsDir + "/" + d)
	}
	return []string{"sh", "-c", b.String()}
}

func removeAppMount(c *ecstypes.ContainerDefinition) {
	out := c.MountPoints[:0]
	for _, m := range c.MountPoints {
		if aws.ToString(m.SourceVolume) != agentVolumeName {
			out = append(out, m)
		}
	}
	c.MountPoints = out
	if len(c.MountPoints) == 0 {
		c.MountPoints = nil
	}
}

func removeDependsOn(c *ecstypes.ContainerDefinition, name string) {
	out := c.DependsOn[:0]
	for _, d := range c.DependsOn {
		if aws.ToString(d.ContainerName) != name {
			out = append(out, d)
		}
	}
	c.DependsOn = out
	if len(c.DependsOn) == 0 {
		c.DependsOn = nil
	}
}

func removeECSContainer(td *ecstypes.TaskDefinition, name string) {
	out := td.ContainerDefinitions[:0]
	for _, c := range td.ContainerDefinitions {
		if aws.ToString(c.Name) != name {
			out = append(out, c)
		}
	}
	td.ContainerDefinitions = out
}

func removeAgentVolume(td *ecstypes.TaskDefinition) {
	out := td.Volumes[:0]
	for _, v := range td.Volumes {
		if aws.ToString(v.Name) != agentVolumeName {
			out = append(out, v)
		}
	}
	td.Volumes = out
	if len(td.Volumes) == 0 {
		td.Volumes = nil
	}
}

func findECSContainer(td *ecstypes.TaskDefinition, name string) *ecstypes.ContainerDefinition {
	for i := range td.ContainerDefinitions {
		if aws.ToString(td.ContainerDefinitions[i].Name) == name {
			return &td.ContainerDefinitions[i]
		}
	}
	return nil
}

func containerHasMount(c *ecstypes.ContainerDefinition) bool {
	for _, m := range c.MountPoints {
		if aws.ToString(m.SourceVolume) == agentVolumeName {
			return true
		}
	}
	return false
}

func containerDependsOnSidecar(c *ecstypes.ContainerDefinition) bool {
	for _, d := range c.DependsOn {
		if aws.ToString(d.ContainerName) == sidecarContainerName && d.Condition == ecstypes.ContainerConditionSuccess {
			return true
		}
	}
	return false
}

func hasAgentVolume(td *ecstypes.TaskDefinition) bool {
	for _, v := range td.Volumes {
		if aws.ToString(v.Name) == agentVolumeName {
			return true
		}
	}
	return false
}

// --- export wiring helpers ---

// ecsResourceAttributes are the resource attributes added to every span/metric/log exported from an
// instrumented Fargate task. service.name is set separately via OTEL_SERVICE_NAME.
func ecsResourceAttributes(region string) string {
	attrs := "cloud.provider=aws,cloud.platform=aws_ecs"
	if region != "" {
		attrs += ",cloud.region=" + region
	}
	return attrs
}

// grpcEndpointURL turns the destination's OTLP gRPC target (host:port) into a URL the OTel SDK env
// accepts, choosing the scheme from the destination's TLS setting. A value that is already a URL is
// passed through unchanged.
func grpcEndpointURL(endpoint string, insecure bool) string {
	if endpoint == "" {
		return ""
	}
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	return scheme + "://" + endpoint
}
