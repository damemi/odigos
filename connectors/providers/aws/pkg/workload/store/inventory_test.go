package store

import "testing"

// TestOdigosInstrumented guards the ownership predicate that gates rollback (issue 1): only our marker or
// our odigos-namespaced collector vars count as Odigos-owned. A customer's own OpenTelemetry/ADOT exec
// wrapper must NOT be treated as ours, or rollback would strip a customer's instrumentation.
func TestOdigosInstrumented(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nil env", nil, false},
		{"empty env", map[string]string{}, false},
		{"marker present", map[string]string{EnvOdigosManaged: `{"v":1}`}, true},
		{"odigos collector endpoint", map[string]string{envCollectorEndpoint: "collector:4317"}, true},
		{"odigos collector insecure", map[string]string{envCollectorInsecure: "true"}, true},
		// Customer's own OTel/ADOT setup — not ours, must be left alone.
		{"customer exec wrapper only", map[string]string{EnvExecWrapper: "/opt/otel-instrument"}, false},
		{"customer otel service name only", map[string]string{"OTEL_SERVICE_NAME": "svc"}, false},
	}
	for _, tc := range cases {
		if got := OdigosInstrumented(tc.env); got != tc.want {
			t.Errorf("%s: OdigosInstrumented = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSplitRuntime(t *testing.T) {
	cases := map[string][2]string{
		"java17":       {"java", "17"},
		"python3.11":   {"python", "3.11"},
		"nodejs20.x":   {"nodejs", "20.x"},
		"ruby3.3":      {"ruby", "3.3"},
		"dotnet8":      {"dotnet", "8"},
		"go1.x":        {"go", "1.x"},
		"provided.al2": {"provided.al", "2"},
		"":             {"", ""},
	}
	for runtime, want := range cases {
		lang, ver := splitRuntime(runtime)
		if lang != want[0] || ver != want[1] {
			t.Errorf("splitRuntime(%q) = (%q,%q), want (%q,%q)", runtime, lang, ver, want[0], want[1])
		}
	}
}

// An ECS service's external id is cluster_arn || '/' || service_name, and a cluster ARN itself ends
// in "cluster/<name>" — so the split must be on the LAST separator. Cutting at the first one yields
// a truncated ARN ("arn:aws:ecs:...:cluster") that matches no cluster.
func TestClusterARNFromExternalID(t *testing.T) {
	cases := map[string]string{
		"arn:aws:ecs:il-central-1:025950698545:cluster/dev/simple-demo": "arn:aws:ecs:il-central-1:025950698545:cluster/dev",
		"arn:aws:ecs:us-east-1:1:cluster/prod/api":                      "arn:aws:ecs:us-east-1:1:cluster/prod",
		// A task ARN (standalone tasks are keyed by it) has no service suffix to strip; the function
		// is only called for service rows, but it must not mangle anything it is handed.
		"no-separator": "no-separator",
	}
	for in, want := range cases {
		if got := clusterARNFromExternalID(in); got != want {
			t.Errorf("clusterARNFromExternalID(%q) = %q, want %q", in, got, want)
		}
	}
}

// The container inventory is what lets a report show the container layer. A row written before the
// inventory existed, or one that is unreadable, must degrade to no containers rather than failing a
// whole workload listing.
func TestDecodeECSContainers(t *testing.T) {
	raw := []byte(`[{"name":"app","image":"app:1","runtime_language":"java","eligible":true,"instrumented":true,"essential":true},
	                {"name":"odigos-agent","odigos_managed":true,"excluded_reason":"Odigos agents sidecar"}]`)
	got := decodeECSContainers(raw)
	if len(got) != 2 {
		t.Fatalf("got %d containers, want 2", len(got))
	}
	if got[0].Name != "app" || got[0].RuntimeLanguage != "java" || !got[0].Eligible || !got[0].Instrumented || !got[0].Essential {
		t.Errorf("app container not decoded: %+v", got[0])
	}
	if !got[1].OdigosManaged || got[1].ExcludedReason == "" {
		t.Errorf("sidecar not decoded: %+v", got[1])
	}
	if decodeECSContainers(nil) != nil {
		t.Error("an absent inventory must decode to no containers")
	}
	if decodeECSContainers([]byte(`{"not":"an array"}`)) != nil {
		t.Error("an unreadable inventory must degrade to no containers, not panic or half-decode")
	}
}

// Details must report only what the store actually holds. A standalone task shares the ECS kind but
// is keyed by task ARN and carries none of the service columns, so it gets no details at all rather
// than a block of empty fields a client cannot tell apart from a real one.
func TestWorkloadDetailsByKind(t *testing.T) {
	svc := workloadDetails(KindECSTask, "arn:aws:ecs:us-east-1:1:cluster/prod/api", "",
		"FARGATE", "api:42", "ECS", []byte(`[{"name":"app"}]`), nil)
	if svc == nil {
		t.Fatal("an ECS service row must produce details")
	}
	if svc.ClusterArn != "arn:aws:ecs:us-east-1:1:cluster/prod" || svc.TaskDefinition != "api:42" ||
		svc.LaunchType != "FARGATE" || svc.DeploymentController != "ECS" || len(svc.Containers) != 1 {
		t.Errorf("service details = %+v", svc)
	}

	if got := workloadDetails(KindECSTask, "arn:aws:ecs:us-east-1:1:task/abc", "", "", "", "", nil, nil); got != nil {
		t.Errorf("standalone task details = %+v, want nil", got)
	}

	// The observed layer set resolves to the ODIGOS layer only. A Lambda report's layerArn has to answer
	// "which Odigos layer version is this function on", so a customer's own layer must never fill it in.
	layers := []string{
		"arn:aws:lambda:us-east-1:1:layer:LambdaAdapterLayerX86:25",
		"arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-arm64:7",
	}
	lam := workloadDetails(KindLambda, "arn:aws:lambda:us-east-1:1:function:fn", "python3.12", "arm64", "", "", nil, layers)
	if lam == nil || lam.Runtime != "python3.12" || lam.Architecture != "arm64" || lam.FunctionArn == "" {
		t.Errorf("lambda details = %+v", lam)
	}
	if lam.LayerArn != layers[1] {
		t.Errorf("layerArn = %q, want the odigos layer %q", lam.LayerArn, layers[1])
	}
	if len(lam.Containers) != 0 {
		t.Errorf("lambda has no container layer, got %+v", lam.Containers)
	}

	// A function carrying only foreign layers reports no Odigos layer rather than the first one it finds.
	foreign := workloadDetails(KindLambda, "arn:aws:lambda:us-east-1:1:function:fn", "python3.12", "arm64", "", "", nil,
		[]string{"arn:aws:lambda:us-east-1:1:layer:AWSOtelPython:5"})
	if foreign.LayerArn != "" {
		t.Errorf("layerArn = %q, want empty for a function with no odigos layer", foreign.LayerArn)
	}
}
