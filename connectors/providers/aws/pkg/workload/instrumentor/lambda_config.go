package instrumentor

import (
	"encoding/json"
	"fmt"
	"slices"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

const EnvLambdaConfig = "ODIGOS_LAMBDA_CONFIG"
const lambdaWebServerWrapper = "/opt/odigos-webserver"
const lambdaConfigVersion = 2

// Wire format shared with aws-connector-artifacts/lambda/config and its CDK package.
// Version 2 uses readable field names; the parser retains v1 support solely for
// safely observing and migrating functions instrumented by an older release.
// Pipeline definitions and SDK defaults live in the layer, not in the Lambda environment.
type lambdaBootstrapConfig struct {
	Version            int               `json:"version"`
	Service            string            `json:"service"`
	Signals            lambdaSignals     `json:"signals"`
	Endpoint           string            `json:"endpoint"`
	HTTP               bool              `json:"http,omitempty"`
	Insecure           bool              `json:"insecure,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	ApplicationSignals bool              `json:"applicationSignals,omitempty"`
	FunctionLogs       bool              `json:"functionLogs,omitempty"`
	Debug              bool              `json:"debug,omitempty"`
	WebWrapper         *string           `json:"webWrapper,omitempty"`
	NoCollector        bool              `json:"noCollector,omitempty"`
}

// The wire format names signals explicitly. Bit flags remain an implementation
// detail for compatibility with configurations written by older layers.
type lambdaSignals int

func (s lambdaSignals) MarshalJSON() ([]byte, error) {
	names := []string{}
	for index, name := range []string{"traces", "metrics", "logs"} {
		if int(s)&(1<<index) != 0 {
			names = append(names, name)
		}
	}
	return json.Marshal(names)
}

func (s *lambdaSignals) UnmarshalJSON(raw []byte) error {
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		// Accept the earlier numeric encoding during migration.
		var mask int
		if err := json.Unmarshal(raw, &mask); err != nil || mask < 1 || mask > 7 {
			return fmt.Errorf("signals must name traces, metrics or logs")
		}
		*s = lambdaSignals(mask)
		return nil
	}
	mask := 0
	for _, name := range names {
		index := slices.Index([]string{"traces", "metrics", "logs"}, name)
		if index < 0 || mask&(1<<index) != 0 {
			return fmt.Errorf("unknown or repeated Lambda signal %q", name)
		}
		mask |= 1 << index
	}
	if mask == 0 {
		return fmt.Errorf("at least one Lambda signal is required")
	}
	*s = lambdaSignals(mask)
	return nil
}

// legacyLambdaBootstrapConfig is the v1 wire format. The layer accepts it during
// rollout so functions carrying a prior configuration remain observable and can
// be safely reinstrumented with the readable v2 format.
type legacyLambdaBootstrapConfig struct {
	Version            int               `json:"v"`
	Service            string            `json:"s"`
	Signals            int               `json:"t"`
	Endpoint           string            `json:"e"`
	HTTP               bool              `json:"p,omitempty"`
	Insecure           bool              `json:"i,omitempty"`
	Headers            map[string]string `json:"h,omitempty"`
	ApplicationSignals bool              `json:"a,omitempty"`
	FunctionLogs       bool              `json:"l,omitempty"`
	Debug              bool              `json:"d,omitempty"`
	WebWrapper         *string           `json:"w,omitempty"`
}

func signalMask(t domaindest.OTLPTarget) lambdaSignals {
	mask := 0
	if t.Traces {
		mask |= 1
	}
	if t.Metrics {
		mask |= 2
	}
	if t.Logs {
		mask |= 4
	}
	return lambdaSignals(mask)
}
func (c lambdaBootstrapConfig) target() domaindest.OTLPTarget {
	t := domaindest.OTLPTarget{Insecure: c.Insecure, Traces: c.Signals&1 != 0, Metrics: c.Signals&2 != 0, Logs: c.Signals&4 != 0}
	if c.HTTP {
		t.HTTPEndpoint = c.Endpoint
	} else {
		t.Endpoint = c.Endpoint
	}
	return t
}
func parseLambdaConfig(raw string) (lambdaBootstrapConfig, error) {
	var envelope struct {
		Version       int `json:"version"`
		LegacyVersion int `json:"v"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return lambdaBootstrapConfig{}, fmt.Errorf("invalid Odigos Lambda configuration JSON")
	}

	var c lambdaBootstrapConfig
	switch {
	case envelope.Version == lambdaConfigVersion:
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, fmt.Errorf("invalid Odigos Lambda configuration JSON")
		}
	case envelope.LegacyVersion == 1:
		var legacy legacyLambdaBootstrapConfig
		if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
			return c, fmt.Errorf("invalid Odigos Lambda configuration JSON")
		}
		c = lambdaBootstrapConfig{
			Version: legacy.Version, Service: legacy.Service, Signals: lambdaSignals(legacy.Signals), Endpoint: legacy.Endpoint,
			HTTP: legacy.HTTP, Insecure: legacy.Insecure, Headers: legacy.Headers, ApplicationSignals: legacy.ApplicationSignals,
			FunctionLogs: legacy.FunctionLogs, Debug: legacy.Debug, WebWrapper: legacy.WebWrapper,
		}
	default:
		return c, fmt.Errorf("invalid Odigos Lambda configuration JSON")
	}
	if (c.Version != 1 && c.Version != lambdaConfigVersion) || c.Signals < 1 || c.Signals > 7 || c.Endpoint == "" {
		return c, fmt.Errorf("invalid Odigos Lambda configuration version, signals or endpoint")
	}
	return c, nil
}
func compactLambdaEnvironment(env map[string]string, m managedManifest, webServer bool, runtime, distro, service string, target domaindest.OTLPTarget, headers map[string]string, debug bool) error {
	recipe := recipeFor(LanguageFromRuntime(runtime), distro)
	c := lambdaBootstrapConfig{Version: lambdaConfigVersion, Service: service, Signals: signalMask(target), Endpoint: target.Endpoint, Insecure: target.Insecure, Headers: headers, Debug: debug && !webServer, ApplicationSignals: recipe.ApplicationSignals, NoCollector: recipe.NoCollector}
	c.FunctionLogs = lambdaFunctionLogsEnabled(target, webServer, runtime, distro)
	if c.Endpoint == "" {
		c.Endpoint = target.HTTPEndpoint
		c.HTTP = true
		c.Insecure = false
	}
	if webServer {
		wrapper := priorEnv(m, store.EnvExecWrapper)
		c.WebWrapper = &wrapper
	}
	// Preserve customers' saved SDK/collector variables. Our child processes override them at startup.
	for key, previous := range m.PrevEnv {
		if previous == nil {
			delete(env, key)
		} else {
			env[key] = *previous
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	env[EnvLambdaConfig] = string(raw)
	if webServer {
		env[store.EnvExecWrapper] = lambdaWebServerWrapper
	} else {
		env[store.EnvExecWrapper] = execWrapperForRuntime(runtime, distro)
	}
	return nil
}

func lambdaFunctionLogs(language, wrapper string) bool {
	if language == LangRuby {
		return true
	}
	if language != LangJava {
		return false
	}
	switch wrapper {
	case "/opt/otel-handler", "/opt/otel-proxy-handler", "/opt/otel-sqs-handler", "/opt/otel-stream-handler":
		return true
	default:
		return false
	}
}

// Expand only for comparison with the existing SDK policy. Customer AWS variables are untouched.
func expandedLambdaEnvironment(env map[string]string, runtime, distro string) (map[string]string, error) {
	c, err := parseLambdaConfig(lambdaConfigFromEnvironment(env))
	if err != nil {
		return nil, err
	}
	cfg, err := renderCollectorConfig(c.target(), c.Headers)
	if err != nil {
		return nil, err
	}
	virtual := cloneLambdaEnv(env)
	delete(virtual, EnvLambdaConfig)
	// The recursive comparison uses the legacy expanded SDK representation.
	for _, key := range []string{store.EnvOdigosManaged, store.EnvOdigosIaC} {
		var marker map[string]json.RawMessage
		if json.Unmarshal([]byte(virtual[key]), &marker) == nil {
			delete(marker, "c")
			raw, _ := json.Marshal(marker)
			virtual[key] = string(raw)
		}
	}
	if c.WebWrapper != nil {
		prior := map[string]*string{store.EnvExecWrapper: c.WebWrapper}
		for _, key := range []string{EnvNodeOptions, EnvPythonPath, EnvJavaToolOptions} {
			if value, ok := env[key]; ok {
				v := value
				prior[key] = &v
			}
		}
		applyWebServerEnv(virtual, managedManifest{PrevEnv: prior}, LanguageFromRuntime(runtime), c.Service, c.target(), cfg)
	} else {
		applyHandlerEnv(virtual, runtime, distro, c.Service, c.target(), c.Debug, cfg)
	}
	return virtual, nil
}

func lambdaConfigFromEnvironment(env map[string]string) string {
	if raw := env[EnvLambdaConfig]; raw != "" {
		return raw
	}
	for _, key := range []string{store.EnvOdigosManaged, store.EnvOdigosIaC} {
		var marker struct {
			Config json.RawMessage `json:"c"`
		}
		if json.Unmarshal([]byte(env[key]), &marker) == nil && len(marker.Config) > 0 {
			return string(marker.Config)
		}
	}
	return ""
}

func restoreLambdaEnvironment(env map[string]string, m managedManifest) {
	compact := m.AppliedFingerprint != "" || lambdaConfigFromEnvironment(env) != ""
	for key, previous := range m.PrevEnv {
		if compact && key != store.EnvExecWrapper && key != EnvLambdaConfig {
			continue
		}
		if previous == nil {
			delete(env, key)
		} else {
			env[key] = *previous
		}
	}
	delete(env, store.EnvOdigosManaged)
	delete(env, store.EnvOdigosIaC)
}
