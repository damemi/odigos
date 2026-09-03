package k8s

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/destinations"
)

func toolReq(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

func TestBoolArgJSONRPCTypes(t *testing.T) {
	if got := boolArg(toolReq(nil), "dry_run", true); !got {
		t.Fatal("missing args must keep the write-tool default of dry_run=true")
	}
	if got := boolArg(toolReq(map[string]any{}), "dry_run", true); !got {
		t.Fatal("absent key must keep dry_run=true so a write is not applied by omission")
	}
	if got := boolArg(toolReq(map[string]any{"dry_run": false}), "dry_run", true); got {
		t.Fatal("explicit false must apply")
	}
	if got := boolArg(toolReq(map[string]any{"dry_run": true}), "dry_run", false); !got {
		t.Fatal("explicit true must apply")
	}
	if got := boolArg(toolReq(map[string]any{"dry_run": "false"}), "dry_run", true); !got {
		t.Fatal("a string must not be coerced; keep the default so a model typo cannot disable dry-run")
	}
}

func TestBoolArgPtrDistinguishesUnsetFromFalse(t *testing.T) {
	if got := boolArgPtr(toolReq(map[string]any{}), "enabled"); got != nil {
		t.Fatal("absent key must be nil, not false")
	}
	got := boolArgPtr(toolReq(map[string]any{"enabled": false}), "enabled")
	if got == nil || *got {
		t.Fatalf("explicit false must be a non-nil false pointer, got %#v", got)
	}
}

func TestIntArgJSONNumbers(t *testing.T) {
	// MCP JSON-RPC decodes numbers as float64. An integer-typed value from a
	// hand-built request must not be silently treated as the default.
	if got := intArg(toolReq(map[string]any{"limit": float64(7)}), "limit", 50); got != 7 {
		t.Fatalf("json number 7: got %d", got)
	}
	if got := intArg(toolReq(map[string]any{}), "limit", 50); got != 50 {
		t.Fatalf("absent: got %d, want default 50", got)
	}
	if got := intArg(toolReq(map[string]any{"limit": "7"}), "limit", 50); got != 50 {
		t.Fatalf("string must not coerce, got %d", got)
	}
}

func TestRequireStringMap(t *testing.T) {
	got, err := requireStringMap(toolReq(map[string]any{
		"fields": map[string]any{"OTLP_ENDPOINT": "https://otlp.example:4317", "API_KEY": "s"},
	}), "fields")
	if err != nil {
		t.Fatalf("valid map: %v", err)
	}
	if got["OTLP_ENDPOINT"] != "https://otlp.example:4317" || got["API_KEY"] != "s" {
		t.Fatalf("got %#v", got)
	}

	if _, err := requireStringMap(toolReq(nil), "fields"); err == nil {
		t.Fatal("missing arguments must error")
	}
	if _, err := requireStringMap(toolReq(map[string]any{}), "fields"); err == nil {
		t.Fatal("missing key must error")
	}
	if _, err := requireStringMap(toolReq(map[string]any{"fields": "not-an-object"}), "fields"); err == nil {
		t.Fatal("string value must error")
	}
	if _, err := requireStringMap(toolReq(map[string]any{
		"fields": map[string]any{"OTLP_ENDPOINT": 4317},
	}), "fields"); err == nil {
		t.Fatal("non-string field must error rather than stringify")
	}
}

func TestNormalizeKindCanonicalizesWorkloadKinds(t *testing.T) {
	cases := map[string]k8sconsts.WorkloadKind{
		"deployment":  k8sconsts.WorkloadKindDeployment,
		"Deployment":  k8sconsts.WorkloadKindDeployment,
		"DEPLOYMENT":  k8sconsts.WorkloadKindDeployment,
		"daemonset":   k8sconsts.WorkloadKindDaemonSet,
		"statefulset": k8sconsts.WorkloadKindStatefulSet,
		"cronjob":     k8sconsts.WorkloadKindCronJob,
		"namespace":   k8sconsts.WorkloadKindNamespace,
	}
	for in, want := range cases {
		if got := normalizeKind(in); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", in, got, want)
		}
	}
	if got := normalizeKind("replicaset"); got != k8sconsts.WorkloadKind("replicaset") {
		t.Errorf("unknown kind must pass through, got %q", got)
	}
}

func TestParseOptionalWorkloadScopeAllOrNothing(t *testing.T) {
	got, err := parseOptionalWorkloadScope(toolReq(map[string]any{}))
	if err != nil || got != nil {
		t.Fatalf("fully unset must be cluster-wide (nil), got %#v err=%v", got, err)
	}

	if _, err := parseOptionalWorkloadScope(toolReq(map[string]any{
		"workload_namespace": "prod",
		"workload_name":      "checkout",
	})); err == nil {
		t.Fatal("partial scope must error so a rule cannot silently apply cluster-wide")
	}

	got, err = parseOptionalWorkloadScope(toolReq(map[string]any{
		"workload_namespace": "prod",
		"workload_kind":      "Deployment",
		"workload_name":      "checkout",
	}))
	if err != nil {
		t.Fatalf("complete scope: %v", err)
	}
	if got == nil || len(got.Sources) != 1 {
		t.Fatalf("complete scope: got %#v", got)
	}
	src := got.Sources[0]
	if src.Namespace != "prod" || src.Name != "checkout" || src.Kind != k8sconsts.WorkloadKindDeployment {
		t.Fatalf("got %#v", src)
	}
}

func TestSplitAndValidateDestinationFields(t *testing.T) {
	schema := &destinations.Destination{}
	schema.Metadata.Type = "otlp"
	schema.Spec.Fields = []destinations.Field{
		{Name: "OTLP_ENDPOINT", Secret: false, ComponentProps: map[string]interface{}{"required": true}},
		{Name: "OTLP_HEADERS", Secret: true},
		{Name: "COMPRESSION", Secret: false},
	}

	data, secret, errs := splitAndValidateDestinationFields(schema, map[string]string{
		"OTLP_ENDPOINT": "https://otlp.example:4317",
		"OTLP_HEADERS":  "authorization=Bearer x",
		"COMPRESSION":   "gzip",
	})
	if len(errs) != 0 {
		t.Fatalf("valid: errs=%v", errs)
	}
	if data["OTLP_ENDPOINT"] != "https://otlp.example:4317" || data["COMPRESSION"] != "gzip" {
		t.Fatalf("plain fields: %#v", data)
	}
	if secret["OTLP_HEADERS"] != "authorization=Bearer x" {
		t.Fatalf("secret partition: %#v", secret)
	}
	if _, ok := data["OTLP_HEADERS"]; ok {
		t.Fatal("secret field must not be stored in plain data")
	}

	_, _, errs = splitAndValidateDestinationFields(schema, map[string]string{})
	if len(errs) == 0 {
		t.Fatal("missing required field must error")
	}

	_, _, errs = splitAndValidateDestinationFields(schema, map[string]string{
		"OTLP_ENDPOINT": "https://otlp.example:4317",
		"UNKNOWN":       "x",
	})
	if len(errs) == 0 {
		t.Fatal("unknown field must error rather than persist")
	}

	data, secret, errs = splitAndValidateDestinationFields(schema, map[string]string{
		"OTLP_ENDPOINT": "https://otlp.example:4317",
		"OTLP_HEADERS":  "",
		"COMPRESSION":   "",
	})
	if len(errs) != 0 {
		t.Fatalf("empty optional: %v", errs)
	}
	if _, ok := secret["OTLP_HEADERS"]; ok {
		t.Fatal("empty secret must not be stored")
	}
	if _, ok := data["COMPRESSION"]; ok {
		t.Fatal("empty optional must not overwrite")
	}
}

func TestParseMetricsSettingsArgUnsetVsFalse(t *testing.T) {
	got, err := parseMetricsSettingsArg(toolReq(map[string]any{}), "metrics_settings")
	if err != nil || got != nil {
		t.Fatalf("absent must be nil so existing Destination metrics flags are left alone, got %#v err=%v", got, err)
	}

	got, err = parseMetricsSettingsArg(toolReq(map[string]any{
		"metrics_settings": map[string]any{
			"spanMetricsEnabled":      false,
			"hostMetricsEnabled":      true,
			"kubeletStatsEnabled":     "yes",
			"serviceGraphEnabled":     true,
			"odigosOwnMetricsEnabled": false,
		},
	}), "metrics_settings")
	if err != nil {
		t.Fatalf("valid object: %v", err)
	}
	if got == nil {
		t.Fatal("provided object must not be nil")
	}
	if got.CollectSpanMetrics == nil || *got.CollectSpanMetrics {
		t.Fatal("explicit false must stay false, not become unset")
	}
	if got.CollectHostMetrics == nil || !*got.CollectHostMetrics {
		t.Fatal("explicit true must stay true")
	}
	if got.CollectKubeletStats != nil {
		t.Fatal("non-bool value must stay unset rather than coerce")
	}
	if got.CollectAgentsTelemetry != nil {
		t.Fatal("omitted flag must stay unset rather than default false")
	}
}
