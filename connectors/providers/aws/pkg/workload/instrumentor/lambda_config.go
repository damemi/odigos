package instrumentor

import (
	"encoding/json"
	"fmt"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

const EnvLambdaConfig = "ODIGOS_LAMBDA_CONFIG"
const lambdaWebServerWrapper = "/opt/odigos-webserver"

// Wire format shared with aws-connector-artifacts/lambda/config and its CDK package.
// Pipeline definitions and SDK defaults live in the layer, not in the Lambda environment.
type lambdaBootstrapConfig struct {
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

func signalMask(t domaindest.OTLPTarget) int {
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
	return mask
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
	var c lambdaBootstrapConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, fmt.Errorf("invalid Odigos Lambda configuration JSON")
	}
	if c.Version != 1 || c.Signals < 1 || c.Signals > 7 || c.Endpoint == "" {
		return c, fmt.Errorf("invalid Odigos Lambda configuration version, signals or endpoint")
	}
	return c, nil
}
func compactLambdaEnvironment(env map[string]string, m managedManifest, webServer bool, runtime, distro, service string, target domaindest.OTLPTarget, headers map[string]string, debug bool) error {
	c := lambdaBootstrapConfig{Version: 1, Service: service, Signals: signalMask(target), Endpoint: target.Endpoint, Insecure: target.Insecure, Headers: headers, Debug: debug && !webServer, ApplicationSignals: recipeFor(LanguageFromRuntime(runtime), distro).ApplicationSignals}
	c.FunctionLogs = target.Logs && !webServer && lambdaFunctionLogs(LanguageFromRuntime(runtime), execWrapperForRuntime(runtime, distro))
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
