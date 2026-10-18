package instrumentor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/workload/store"
)

const (
	EnvServiceName        = "OTEL_SERVICE_NAME"
	EnvResourceAttributes = "OTEL_RESOURCE_ATTRIBUTES"
	EnvOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvOTLPProtocol       = "OTEL_EXPORTER_OTLP_PROTOCOL"
	EnvTracesExporter     = "OTEL_TRACES_EXPORTER"
	EnvMetricsExporter    = "OTEL_METRICS_EXPORTER"
	EnvLogsExporter       = "OTEL_LOGS_EXPORTER"
	EnvOtelLogLevel       = "OTEL_LOG_LEVEL"
	// EnvCollectorEndpoint is ODIGOS_OTLP_ENDPOINT: the destination as host:port.
	// Today the agent ignores it and uses EnvOTLPEndpoint (standard OTel). We still
	// set it to record the configured destination; future Odigos agents will read it
	// and EnvOTLPEndpoint will be dropped.
	EnvCollectorEndpoint = store.EnvCollectorEndpoint

	exporterOTLP      = "otlp"
	exporterNone      = "none"
	otlpProtocolGRPC  = "grpc"
	otelLogLevelDebug = "debug"

	initContainerName = "odigos-agent"
	agentVolumeName   = "odigos-agent"
)

var errUnsupportedLanguage = errors.New(baseapi.ErrPrefixUnsupportedRuntime)

func applyInstrumentation(app *armappcontainers.ContainerApp, lang, image, serviceName, region string, dest domaindest.OTLPTarget, debug bool) error {
	spec, ok := languageSpecs[lang]
	if !ok {
		return fmt.Errorf("%w %q: no Odigos OpenTelemetry agent for this Container Apps language", errUnsupportedLanguage, lang)
	}
	ensureTemplate(app)
	c := ownedOrPrimaryContainer(app)
	if c == nil {
		return fmt.Errorf("container app %q has no container to instrument", serviceName)
	}
	if c.Name == nil || *c.Name == "" {
		c.Name = to.Ptr(defaultContainerName(serviceName))
	}

	env := containerEnvMap(c)
	manifest := buildManifest(env, env[EnvOdigosManaged], lang)

	for k, v := range spec.staticEnv {
		setContainerEnv(c, k, v)
	}
	for _, a := range spec.appendEnv {
		setContainerEnv(c, a.name, addLoaderValue(priorEnv(manifest, a.name), a.value, a.sep))
	}

	setContainerEnv(c, EnvServiceName, serviceName)
	setContainerEnv(c, EnvResourceAttributes, resourceAttributes(region))
	setContainerEnv(c, EnvCollectorEndpoint, dest.Endpoint)
	setContainerEnv(c, EnvOTLPEndpoint, otlpEndpointURL(dest))
	setContainerEnv(c, EnvOTLPProtocol, otlpProtocolGRPC)
	setContainerEnv(c, EnvTracesExporter, exporterFor(dest.Traces))
	setContainerEnv(c, EnvMetricsExporter, exporterFor(dest.Metrics))
	setContainerEnv(c, EnvLogsExporter, exporterFor(dest.Logs))
	if debug {
		setContainerEnv(c, EnvOtelLogLevel, otelLogLevelDebug)
	} else {
		deleteContainerEnv(c, EnvOtelLogLevel)
	}

	marker, err := marshalManifest(manifest)
	if err != nil {
		return err
	}
	setContainerEnv(c, EnvOdigosManaged, marker)

	ensureAgentVolume(app)
	ensureVolumeMount(c)
	ensureInitContainer(app, image, spec.agentDirs)
	return nil
}

func alreadyInstrumented(app *armappcontainers.ContainerApp, lang, image, serviceName, region string, dest domaindest.OTLPTarget, debug bool) bool {
	spec, ok := languageSpecs[lang]
	if !ok {
		return false
	}
	c := ownedOrPrimaryContainer(app)
	if c == nil {
		return false
	}
	env := containerEnvMap(c)
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
	if !containerHasMount(c, agentVolumeName) || !hasVolume(app, agentVolumeName) {
		return false
	}
	ic := findInitContainer(app, initContainerName)
	return ic != nil && deref(ic.Image) == image
}

func rollbackInstrumentation(app *armappcontainers.ContainerApp) bool {
	c := ownedContainer(app)
	if c == nil {
		return false
	}
	marker, _ := containerEnvValue(c, EnvOdigosManaged)
	manifest, ok := parseManifest(marker)
	if !ok {
		return false
	}
	for k, prev := range manifest.PrevEnv {
		if prev == nil {
			deleteContainerEnv(c, k)
		} else {
			setContainerEnv(c, k, *prev)
		}
	}
	deleteContainerEnv(c, EnvOdigosManaged)
	// Delete unconditionally rather than relying on the snapshot: an app instrumented before this var
	// existed has a marker whose PrevEnv does not mention it, and leaving it behind would keep the detector
	// reporting the app as Odigos-instrumented forever after a rollback.
	deleteContainerEnv(c, EnvCollectorEndpoint)
	removeVolumeMount(c, agentVolumeName)
	removeInitContainer(app, initContainerName)
	removeVolume(app, agentVolumeName)
	return true
}

func appOwned(app *armappcontainers.ContainerApp) bool { return ownedContainer(app) != nil }

func exporterFor(supported bool) string {
	return domaindest.ExporterFor(supported)
}

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

func resourceAttributes(region string) string {
	attrs := "cloud.provider=azure,cloud.platform=azure_container_apps"
	if region != "" {
		attrs += ",cloud.region=" + region
	}
	return attrs
}

func addLoaderValue(original, value, sep string) string {
	if original == "" {
		return value
	}
	if strings.Contains(original, value) {
		return original
	}
	return original + sep + value
}

func defaultContainerName(serviceName string) string {
	if serviceName == "" {
		return "app"
	}
	return serviceName
}

func ensureTemplate(app *armappcontainers.ContainerApp) {
	if app.Properties == nil {
		app.Properties = &armappcontainers.ContainerAppProperties{}
	}
	if app.Properties.Template == nil {
		app.Properties.Template = &armappcontainers.Template{}
	}
}

func ownedContainer(app *armappcontainers.ContainerApp) *armappcontainers.Container {
	if app.Properties == nil || app.Properties.Template == nil {
		return nil
	}
	for _, c := range app.Properties.Template.Containers {
		if c == nil {
			continue
		}
		if _, ok := containerEnvValue(c, EnvOdigosManaged); ok {
			return c
		}
	}
	return nil
}

func ownedOrPrimaryContainer(app *armappcontainers.ContainerApp) *armappcontainers.Container {
	if c := ownedContainer(app); c != nil {
		return c
	}
	return primaryContainer(app)
}

func primaryContainer(app *armappcontainers.ContainerApp) *armappcontainers.Container {
	if app.Properties == nil || app.Properties.Template == nil {
		return nil
	}
	for _, c := range app.Properties.Template.Containers {
		if c != nil {
			return c
		}
	}
	return nil
}

func findInitContainer(app *armappcontainers.ContainerApp, name string) *armappcontainers.InitContainer {
	if app.Properties == nil || app.Properties.Template == nil {
		return nil
	}
	for _, c := range app.Properties.Template.InitContainers {
		if c != nil && c.Name != nil && *c.Name == name {
			return c
		}
	}
	return nil
}

func containerEnvValue(c *armappcontainers.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e != nil && e.Name != nil && *e.Name == name && e.Value != nil {
			return *e.Value, true
		}
	}
	return "", false
}

func containerEnvMap(c *armappcontainers.Container) map[string]string {
	out := make(map[string]string, len(c.Env))
	for _, e := range c.Env {
		if e == nil || e.Name == nil || e.Value == nil || e.SecretRef != nil {
			continue
		}
		out[*e.Name] = *e.Value
	}
	return out
}

func setContainerEnv(c *armappcontainers.Container, name, value string) {
	for _, e := range c.Env {
		if e != nil && e.Name != nil && *e.Name == name {
			e.Value = to.Ptr(value)
			e.SecretRef = nil
			return
		}
	}
	c.Env = append(c.Env, &armappcontainers.EnvironmentVar{Name: to.Ptr(name), Value: to.Ptr(value)})
}

func deleteContainerEnv(c *armappcontainers.Container, name string) {
	out := c.Env[:0]
	for _, e := range c.Env {
		if e == nil || e.Name == nil || *e.Name != name {
			out = append(out, e)
		}
	}
	c.Env = out
}

func ensureAgentVolume(app *armappcontainers.ContainerApp) {
	tmpl := app.Properties.Template
	st := armappcontainers.StorageTypeEmptyDir
	desired := &armappcontainers.Volume{Name: to.Ptr(agentVolumeName), StorageType: &st}
	for i, v := range tmpl.Volumes {
		if v != nil && v.Name != nil && *v.Name == agentVolumeName {
			tmpl.Volumes[i] = desired
			return
		}
	}
	tmpl.Volumes = append(tmpl.Volumes, desired)
}

func ensureVolumeMount(c *armappcontainers.Container) {
	for _, m := range c.VolumeMounts {
		if m != nil && m.VolumeName != nil && *m.VolumeName == agentVolumeName {
			m.MountPath = to.Ptr(agentsDir)
			return
		}
	}
	c.VolumeMounts = append(c.VolumeMounts, &armappcontainers.VolumeMount{
		VolumeName: to.Ptr(agentVolumeName),
		MountPath:  to.Ptr(agentsDir),
	})
}

func ensureInitContainer(app *armappcontainers.ContainerApp, image string, agentDirs []string) {
	tmpl := app.Properties.Template
	cmd := initCommand(agentDirs)
	desired := &armappcontainers.InitContainer{
		Name:    to.Ptr(initContainerName),
		Image:   to.Ptr(image),
		Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr(cmd)},
		VolumeMounts: []*armappcontainers.VolumeMount{{
			VolumeName: to.Ptr(agentVolumeName),
			MountPath:  to.Ptr(agentsDir),
		}},
	}
	for i, c := range tmpl.InitContainers {
		if c != nil && c.Name != nil && *c.Name == initContainerName {
			tmpl.InitContainers[i] = desired
			return
		}
	}
	tmpl.InitContainers = append(tmpl.InitContainers, desired)
}

func initCommand(agentDirs []string) string {
	var b strings.Builder
	b.WriteString("mkdir -p " + agentsDir)
	for _, d := range agentDirs {
		b.WriteString(" && cp -r " + imageAgentsDir + "/" + d + " " + agentsDir + "/" + d)
	}
	return b.String()
}

func removeVolumeMount(c *armappcontainers.Container, name string) {
	out := c.VolumeMounts[:0]
	for _, m := range c.VolumeMounts {
		if m == nil || m.VolumeName == nil || *m.VolumeName != name {
			out = append(out, m)
		}
	}
	c.VolumeMounts = out
}

func removeInitContainer(app *armappcontainers.ContainerApp, name string) {
	tmpl := app.Properties.Template
	out := tmpl.InitContainers[:0]
	for _, c := range tmpl.InitContainers {
		if c == nil || c.Name == nil || *c.Name != name {
			out = append(out, c)
		}
	}
	tmpl.InitContainers = out
}

func removeVolume(app *armappcontainers.ContainerApp, name string) {
	tmpl := app.Properties.Template
	out := tmpl.Volumes[:0]
	for _, v := range tmpl.Volumes {
		if v == nil || v.Name == nil || *v.Name != name {
			out = append(out, v)
		}
	}
	tmpl.Volumes = out
}

func containerHasMount(c *armappcontainers.Container, name string) bool {
	for _, m := range c.VolumeMounts {
		if m != nil && m.VolumeName != nil && *m.VolumeName == name {
			return true
		}
	}
	return false
}

func hasVolume(app *armappcontainers.ContainerApp, name string) bool {
	if app.Properties == nil || app.Properties.Template == nil {
		return false
	}
	for _, v := range app.Properties.Template.Volumes {
		if v != nil && v.Name != nil && *v.Name == name {
			return true
		}
	}
	return false
}
