package instrumentor

import (
	"errors"
	"fmt"
	"strings"

	runpb "cloud.google.com/go/run/apiv2/runpb"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/workload/store"
)

// OpenTelemetry configuration + identity env vars the instrumentor sets on the ingress container. Unlike
// Lambda (which exports to an in-Lambda collector on localhost), Cloud Run has no node-local collector,
// so the SDK/agent exports OTLP directly to the connector's destination.
const (
	EnvServiceName        = "OTEL_SERVICE_NAME"
	EnvResourceAttributes = "OTEL_RESOURCE_ATTRIBUTES"
	EnvOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvOTLPProtocol       = "OTEL_EXPORTER_OTLP_PROTOCOL"
	EnvTracesExporter     = "OTEL_TRACES_EXPORTER"
	EnvMetricsExporter    = "OTEL_METRICS_EXPORTER"
	EnvLogsExporter       = "OTEL_LOGS_EXPORTER"
	EnvOtelLogLevel       = "OTEL_LOG_LEVEL"
	// EnvCollectorEndpoint carries the user-configured destination as raw host:port. Currently UNUSED by
	// anything on the container — the injected agent is an upstream OpenTelemetry build and reads
	// EnvOTLPEndpoint above — and it is NOT an ownership signal (EnvOdigosManaged alone is). It is set as a
	// mark of the destination we configured, and becomes the agent's actual input once the agents move to an
	// Odigos implementation, at which point EnvOTLPEndpoint drops out of the injected set. See
	// store.EnvCollectorEndpoint.
	EnvCollectorEndpoint = store.EnvCollectorEndpoint

	exporterOTLP      = "otlp"
	exporterNone      = "none"
	otlpProtocolGRPC  = "grpc"
	otelLogLevelDebug = "debug"
)

// Sidecar + shared-volume wiring. This is the OSS Odigos "k8s-init-container" mount method adapted for
// Cloud Run: the agent copier stages the language agent files into an in-memory volume the ingress
// container mounts at /var/odigos (mirroring the OSS EmptyDir). Because Cloud Run has no run-to-completion
// init containers, the copier runs as a startup-ordered sidecar (holds startupProbePort after copying) and
// the ingress container DependsOn it, so the app starts only once staging is done.
const (
	sidecarContainerName = "odigos-agent"
	agentVolumeName      = "odigos-agent"
	// agentVolumeSizeLimit bounds the in-memory volume; the agent files are tens–hundreds of MB depending
	// on language. Counts against the instance memory, so it is deliberately modest.
	agentVolumeSizeLimit = "500Mi"
	// startupProbePort is the TCP port the sidecar holds open once the copy completes; Cloud Run gates the
	// ingress container's start on this probe succeeding (container-dependency ordering).
	startupProbePort = 2400
)

// errUnsupportedLanguage marks a workload whose language has no Odigos Cloud Run agent. Its message is
// prefixed with baseapi.ErrPrefixUnsupportedRuntime so the workload report classifies it as Unsupported
// rather than a retryable failure.
var errUnsupportedLanguage = errors.New(baseapi.ErrPrefixUnsupportedRuntime)

// applyInstrumentation mutates svc into the desired instrumented state for lang: it configures the
// ingress container (agent activation + OTLP export env + ownership marker + shared-volume mount +
// startup dependency) and adds the init-ordered agent sidecar and shared in-memory volume. It is
// idempotent — re-applying the same desired state yields the same service.
func applyInstrumentation(svc *runpb.Service, lang, image, serviceName, region string, dest domaindest.OTLPTarget, debug bool) error {
	spec, ok := languageSpecs[lang]
	if !ok {
		return fmt.Errorf("%w %q: no Odigos OpenTelemetry agent for this Cloud Run language", errUnsupportedLanguage, lang)
	}
	app := ownedOrIngressContainer(svc)
	if app == nil {
		return fmt.Errorf("cloud run service %q has no container to instrument", serviceName)
	}
	// A multi-container service requires every container to be named; name the ingress container if the
	// service was created single-container (name omitted) before we add the sidecar.
	if app.GetName() == "" {
		app.Name = defaultIngressName(serviceName)
	}

	env := containerEnvMap(app)
	manifest := buildManifest(env, env[EnvOdigosManaged], lang)

	for k, v := range spec.staticEnv {
		setContainerEnv(app, k, v)
	}
	for _, a := range spec.appendEnv {
		// Rebuild from the customer's ORIGINAL value (snapshot), not the current one, so a re-instrument
		// never accumulates our value.
		setContainerEnv(app, a.name, addLoaderValue(priorEnv(manifest, a.name), a.value, a.sep))
	}

	setContainerEnv(app, EnvServiceName, serviceName)
	setContainerEnv(app, EnvResourceAttributes, resourceAttributes(region))
	setContainerEnv(app, EnvCollectorEndpoint, dest.Endpoint)
	setContainerEnv(app, EnvOTLPEndpoint, otlpEndpointURL(dest))
	setContainerEnv(app, EnvOTLPProtocol, otlpProtocolGRPC)
	setContainerEnv(app, EnvTracesExporter, exporterFor(dest.Traces))
	setContainerEnv(app, EnvMetricsExporter, exporterFor(dest.Metrics))
	setContainerEnv(app, EnvLogsExporter, exporterFor(dest.Logs))
	if debug {
		setContainerEnv(app, EnvOtelLogLevel, otelLogLevelDebug)
	} else {
		deleteContainerEnv(app, EnvOtelLogLevel)
	}

	marker, err := marshalManifest(manifest)
	if err != nil {
		return err
	}
	setContainerEnv(app, EnvOdigosManaged, marker)

	ensureAgentVolume(svc)
	ensureVolumeMount(app)
	ensureDependsOn(app, sidecarContainerName)
	ensureSidecar(svc, image, spec.agentDirs)
	return nil
}

// alreadyInstrumented reports whether svc already carries the exact desired instrumentation for lang,
// so the reconcile can skip the UpdateService (avoiding a new revision every cycle). It checks the
// managed state rather than proto-equality so Cloud-Run-populated defaults never force spurious updates.
func alreadyInstrumented(svc *runpb.Service, lang, image, serviceName, region string, dest domaindest.OTLPTarget, debug bool) bool {
	spec, ok := languageSpecs[lang]
	if !ok {
		return false
	}
	app := ownedOrIngressContainer(svc)
	if app == nil {
		return false
	}
	env := containerEnvMap(app)
	if _, ok := env[EnvOdigosManaged]; !ok {
		return false
	}
	if env[EnvServiceName] != serviceName ||
		env[EnvResourceAttributes] != resourceAttributes(region) ||
		env[EnvCollectorEndpoint] != dest.Endpoint ||
		env[EnvOTLPEndpoint] != otlpEndpointURL(dest) ||
		env[EnvOTLPProtocol] != otlpProtocolGRPC ||
		env[EnvTracesExporter] != exporterFor(dest.Traces) ||
		env[EnvMetricsExporter] != exporterFor(dest.Metrics) ||
		env[EnvLogsExporter] != exporterFor(dest.Logs) {
		return false
	}
	wantLogLevel := ""
	if debug {
		wantLogLevel = otelLogLevelDebug
	}
	if env[EnvOtelLogLevel] != wantLogLevel {
		return false
	}
	for k, v := range spec.staticEnv {
		if env[k] != v {
			return false
		}
	}
	for _, a := range spec.appendEnv {
		if !strings.Contains(env[a.name], a.value) {
			return false
		}
	}
	if !containerHasMount(app, agentVolumeName) || !containerDependsOn(app, sidecarContainerName) {
		return false
	}
	if !hasVolume(svc, agentVolumeName) {
		return false
	}
	sc := findContainer(svc, sidecarContainerName)
	return sc != nil && sc.GetImage() == image
}

// rollbackInstrumentation restores svc to its pre-Odigos state from the ownership marker. It returns
// false when the service carries no Odigos ownership marker (never ours — left completely untouched).
func rollbackInstrumentation(svc *runpb.Service) bool {
	app := ownedContainer(svc)
	if app == nil {
		return false
	}
	marker, _ := containerEnvValue(app, EnvOdigosManaged)
	manifest, ok := parseManifest(marker)
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
	deleteContainerEnv(app, EnvOdigosManaged)
	// Delete unconditionally rather than relying on the snapshot: a service instrumented before this var
	// existed has a marker whose PrevEnv does not mention it, and leaving it behind would keep the detector
	// reporting the service as Odigos-instrumented forever after a rollback.
	deleteContainerEnv(app, EnvCollectorEndpoint)
	removeVolumeMount(app, agentVolumeName)
	removeDependsOn(app, sidecarContainerName)
	removeContainer(svc, sidecarContainerName)
	removeVolume(svc, agentVolumeName)
	return true
}

// serviceOwned reports whether svc carries Odigos-applied instrumentation (the ownership marker on any
// container). Discovery derives the inventory Instrumented flag from the same signal.
func serviceOwned(svc *runpb.Service) bool { return ownedContainer(svc) != nil }

// --- helpers ---

func exporterFor(supported bool) string {
	return domaindest.ExporterFor(supported)
}

// otlpEndpointURL turns the connector's OTLP export target (host:port from the destination) into a URL
// the OTel SDK accepts, choosing the scheme from the destination's TLS setting. A value that is already a
// URL is passed through unchanged.
func otlpEndpointURL(dest domaindest.OTLPTarget) string {
	if dest.Endpoint == "" {
		return ""
	}
	if strings.Contains(dest.Endpoint, "://") {
		return dest.Endpoint
	}
	scheme := "https"
	if dest.Insecure {
		scheme = "http"
	}
	return scheme + "://" + dest.Endpoint
}

// resourceAttributes are the Cloud Run resource attributes added to every span/metric/log. service.name
// is set separately via OTEL_SERVICE_NAME.
func resourceAttributes(region string) string {
	attrs := "cloud.provider=gcp,cloud.platform=gcp_cloud_run"
	if region != "" {
		attrs += ",cloud.region=" + region
	}
	return attrs
}

// addLoaderValue idempotently appends our loader value after the customer's original env value, joined by
// sep. The original value is kept first to match the OSS Odigos community distro YAMLs, whose append
// patterns are all "{{ORIGINAL_ENV_VALUE}}<sep>{{odigos value}}" (e.g. JAVA_TOOL_OPTIONS, NODE_OPTIONS,
// PYTHONPATH) — so the customer's own settings take precedence and our agent is added on top.
func addLoaderValue(original, value, sep string) string {
	if original == "" {
		return value
	}
	if strings.Contains(original, value) {
		return original
	}
	return original + sep + value
}

func defaultIngressName(serviceName string) string {
	if serviceName == "" {
		return "app"
	}
	return serviceName
}

// --- container / env accessors ---

// ownedContainer returns the container carrying our ownership marker, or nil.
func ownedContainer(svc *runpb.Service) *runpb.Container {
	for _, c := range svc.GetTemplate().GetContainers() {
		if _, ok := containerEnvValue(c, EnvOdigosManaged); ok {
			return c
		}
	}
	return nil
}

// ownedOrIngressContainer returns the marker-carrying container if instrumented, else the ingress
// container (so first-time instrumentation targets the right one).
func ownedOrIngressContainer(svc *runpb.Service) *runpb.Container {
	if c := ownedContainer(svc); c != nil {
		return c
	}
	return ingressContainer(svc)
}

// ingressContainer returns the container that serves ingress: the one with a port, else the sole
// non-sidecar container, else the first non-sidecar container.
func ingressContainer(svc *runpb.Service) *runpb.Container {
	containers := svc.GetTemplate().GetContainers()
	for _, c := range containers {
		if c.GetName() == sidecarContainerName {
			continue
		}
		if len(c.GetPorts()) > 0 {
			return c
		}
	}
	for _, c := range containers {
		if c.GetName() != sidecarContainerName {
			return c
		}
	}
	return nil
}

func findContainer(svc *runpb.Service, name string) *runpb.Container {
	for _, c := range svc.GetTemplate().GetContainers() {
		if c.GetName() == name {
			return c
		}
	}
	return nil
}

func containerEnvValue(c *runpb.Container, name string) (string, bool) {
	for _, e := range c.GetEnv() {
		if e.GetName() == name {
			return e.GetValue(), true
		}
	}
	return "", false
}

// containerEnvMap returns the container's literal env vars (value-source/secret env are skipped — we
// never manage those keys).
func containerEnvMap(c *runpb.Container) map[string]string {
	out := make(map[string]string, len(c.GetEnv()))
	for _, e := range c.GetEnv() {
		if _, ok := e.GetValues().(*runpb.EnvVar_Value); ok {
			out[e.GetName()] = e.GetValue()
		}
	}
	return out
}

// setContainerEnv sets a literal env var, replacing an existing entry in place (preserving order) or
// appending a new one.
func setContainerEnv(c *runpb.Container, name, value string) {
	for _, e := range c.Env {
		if e.GetName() == name {
			e.Values = &runpb.EnvVar_Value{Value: value}
			return
		}
	}
	c.Env = append(c.Env, &runpb.EnvVar{Name: name, Values: &runpb.EnvVar_Value{Value: value}})
}

func deleteContainerEnv(c *runpb.Container, name string) {
	out := c.Env[:0]
	for _, e := range c.Env {
		if e.GetName() != name {
			out = append(out, e)
		}
	}
	c.Env = out
}

// --- volume / mount / dependency mutation ---

func ensureAgentVolume(svc *runpb.Service) {
	tmpl := svc.GetTemplate()
	desired := &runpb.Volume{
		Name: agentVolumeName,
		VolumeType: &runpb.Volume_EmptyDir{EmptyDir: &runpb.EmptyDirVolumeSource{
			Medium:    runpb.EmptyDirVolumeSource_MEMORY,
			SizeLimit: agentVolumeSizeLimit,
		}},
	}
	for i, v := range tmpl.Volumes {
		if v.GetName() == agentVolumeName {
			tmpl.Volumes[i] = desired
			return
		}
	}
	tmpl.Volumes = append(tmpl.Volumes, desired)
}

func ensureVolumeMount(c *runpb.Container) {
	for _, m := range c.VolumeMounts {
		if m.GetName() == agentVolumeName {
			m.MountPath = agentsDir
			return
		}
	}
	c.VolumeMounts = append(c.VolumeMounts, &runpb.VolumeMount{Name: agentVolumeName, MountPath: agentsDir})
}

func ensureDependsOn(c *runpb.Container, name string) {
	for _, d := range c.DependsOn {
		if d == name {
			return
		}
	}
	c.DependsOn = append(c.DependsOn, name)
}

func ensureSidecar(svc *runpb.Service, image string, agentDirs []string) {
	tmpl := svc.GetTemplate()
	desired := &runpb.Container{
		Name:         sidecarContainerName,
		Image:        image,
		Command:      sidecarCommand(agentDirs),
		VolumeMounts: []*runpb.VolumeMount{{Name: agentVolumeName, MountPath: agentsDir}},
		StartupProbe: &runpb.Probe{
			PeriodSeconds:    3,
			TimeoutSeconds:   1,
			FailureThreshold: 30,
			ProbeType:        &runpb.Probe_TcpSocket{TcpSocket: &runpb.TCPSocketAction{Port: startupProbePort}},
		},
	}
	for i, c := range tmpl.Containers {
		if c.GetName() == sidecarContainerName {
			tmpl.Containers[i] = desired
			return
		}
	}
	tmpl.Containers = append(tmpl.Containers, desired)
}

// sidecarCommand copies the language agent directories from the odigos-agents image into the shared
// volume, then holds a TCP port so the ingress container (which depends on this sidecar) starts only once
// the files are staged. The odigos-agents image is busybox-based, so sh/cp/nc are available.
func sidecarCommand(agentDirs []string) []string {
	var b strings.Builder
	b.WriteString("mkdir -p " + agentsDir)
	for _, d := range agentDirs {
		b.WriteString(" && cp -r " + imageAgentsDir + "/" + d + " " + agentsDir + "/" + d)
	}
	fmt.Fprintf(&b, " && while true; do nc -l -p %d >/dev/null 2>&1; done", startupProbePort)
	return []string{"sh", "-c", b.String()}
}

func removeVolumeMount(c *runpb.Container, name string) {
	out := c.VolumeMounts[:0]
	for _, m := range c.VolumeMounts {
		if m.GetName() != name {
			out = append(out, m)
		}
	}
	c.VolumeMounts = out
}

func removeDependsOn(c *runpb.Container, name string) {
	out := c.DependsOn[:0]
	for _, d := range c.DependsOn {
		if d != name {
			out = append(out, d)
		}
	}
	c.DependsOn = out
}

func removeContainer(svc *runpb.Service, name string) {
	tmpl := svc.GetTemplate()
	out := tmpl.Containers[:0]
	for _, c := range tmpl.Containers {
		if c.GetName() != name {
			out = append(out, c)
		}
	}
	tmpl.Containers = out
}

func removeVolume(svc *runpb.Service, name string) {
	tmpl := svc.GetTemplate()
	out := tmpl.Volumes[:0]
	for _, v := range tmpl.Volumes {
		if v.GetName() != name {
			out = append(out, v)
		}
	}
	tmpl.Volumes = out
}

func containerHasMount(c *runpb.Container, name string) bool {
	for _, m := range c.GetVolumeMounts() {
		if m.GetName() == name {
			return true
		}
	}
	return false
}

func containerDependsOn(c *runpb.Container, name string) bool {
	for _, d := range c.GetDependsOn() {
		if d == name {
			return true
		}
	}
	return false
}

func hasVolume(svc *runpb.Service, name string) bool {
	for _, v := range svc.GetTemplate().GetVolumes() {
		if v.GetName() == name {
			return true
		}
	}
	return false
}
