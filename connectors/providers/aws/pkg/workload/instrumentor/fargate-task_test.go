package instrumentor

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

func TestExportTargetFor(t *testing.T) {
	// A distro that prefers gRPC and is not pinned (hypothetical today — every community distro declares
	// OTLP/HTTP — but the fallback must stay symmetric).
	grpcRecipe := agentRecipe{lang: LangJava, otlpProtocol: otlpProtocolGRPC}
	// An http/protobuf distro that is NOT pinned: prefers HTTP, but can be switched (java/python/nodejs/dotnet).
	httpPreferred := agentRecipe{lang: LangPython, otlpProtocol: otlpProtocolHTTP}
	// A PINNED http/protobuf distro (php/ruby) — cannot speak gRPC at all.
	httpPinned := agentRecipe{lang: LangPHP, otlpProtocol: otlpProtocolHTTP, protocolFixed: true}

	both := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, HTTPEndpoint: "http://collector:4318", Traces: true}
	grpcOnly := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: false, Traces: true}
	httpOnly := domaindest.OTLPTarget{HTTPEndpoint: "http://collector:4318", Traces: true, Metrics: true}

	// Each recipe takes its preferred transport when the destination offers both.
	got, err := exportTargetFor(grpcRecipe, both)
	if err != nil || got.endpoint != "http://collector:4317" || got.protocol != otlpProtocolGRPC {
		t.Errorf("grpc/both = %+v err=%v", got, err)
	}
	got, err = exportTargetFor(httpPreferred, both)
	if err != nil || got.endpoint != "http://collector:4318" || got.protocol != otlpProtocolHTTP {
		t.Errorf("http-preferred/both = %+v err=%v", got, err)
	}
	// TLS destination → https scheme on the gRPC endpoint.
	got, err = exportTargetFor(grpcRecipe, grpcOnly)
	if err != nil || got.endpoint != "https://collector:4317" {
		t.Errorf("grpc/tls = %+v err=%v", got, err)
	}
	// Unpinned recipes fall back to whichever transport exists, and the protocol follows the endpoint.
	got, err = exportTargetFor(grpcRecipe, httpOnly)
	if err != nil || got.endpoint != "http://collector:4318" || got.protocol != otlpProtocolHTTP {
		t.Errorf("grpc/http-only fallback = %+v err=%v", got, err)
	}
	if !got.metrics || !got.traces || got.logs {
		t.Errorf("signals not carried through: %+v", got)
	}
	got, err = exportTargetFor(httpPreferred, grpcOnly)
	if err != nil || got.endpoint != "https://collector:4317" || got.protocol != otlpProtocolGRPC {
		t.Errorf("http-preferred/grpc-only fallback = %+v err=%v", got, err)
	}
	// A pinned recipe uses its transport when available...
	got, err = exportTargetFor(httpPinned, both)
	if err != nil || got.endpoint != "http://collector:4318" || got.protocol != otlpProtocolHTTP {
		t.Errorf("php/both = %+v err=%v", got, err)
	}
	// ...and errors rather than silently exporting over one its SDK cannot speak.
	if _, err = exportTargetFor(httpPinned, grpcOnly); err == nil || !strings.Contains(err.Error(), "OTLP_HTTP_ENDPOINT") {
		t.Errorf("php against a gRPC-only destination must error naming the missing endpoint, got %v", err)
	}
	// No endpoint at all.
	if _, err = exportTargetFor(grpcRecipe, domaindest.OTLPTarget{Traces: true}); err == nil {
		t.Error("no endpoints must error")
	}
}

// The misconfiguration that cost a live debugging session: an otlphttp destination pointed at :4317.
// Endpoint and protocol are individually valid, so nothing fails until the agent's first export resets
// the connection inside the customer's task.
func TestPortProtocolMismatch(t *testing.T) {
	if w := portProtocolMismatch("http://jaeger:4317", otlpProtocolHTTP); w == "" || !strings.Contains(w, "4318") {
		t.Errorf("http-on-4317 must warn and name the right port, got %q", w)
	}
	if w := portProtocolMismatch("https://collector:4318", otlpProtocolGRPC); w == "" || !strings.Contains(w, "4317") {
		t.Errorf("grpc-on-4318 must warn and name the right port, got %q", w)
	}
	// Correct pairings and non-standard ports stay silent (we cannot judge a custom port).
	for _, tc := range []struct{ endpoint, protocol string }{
		{"http://jaeger:4318", otlpProtocolHTTP},
		{"http://jaeger:4317", otlpProtocolGRPC},
		{"http://collector:9411", otlpProtocolHTTP},
		{"http://collector", otlpProtocolHTTP},
		{"", otlpProtocolGRPC},
	} {
		if w := portProtocolMismatch(tc.endpoint, tc.protocol); w != "" {
			t.Errorf("portProtocolMismatch(%q,%q) = %q, want no warning", tc.endpoint, tc.protocol, w)
		}
	}
}

func TestEndpointPort(t *testing.T) {
	cases := map[string]string{
		"http://h:4318":       "4318",
		"https://h:4317/v1":   "4317",
		"h:4318":              "4318",
		"http://h":            "",
		"":                    "",
		"http://h:4318/v1/tr": "4318",
	}
	for in, want := range cases {
		if got := endpointPort(in); got != want {
			t.Errorf("endpointPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRolloutInProgress(t *testing.T) {
	primary := func(state ecstypes.DeploymentRolloutState) ecstypes.Deployment {
		return ecstypes.Deployment{Status: aws.String("PRIMARY"), RolloutState: state}
	}
	svc := &ecstypes.Service{Deployments: []ecstypes.Deployment{primary(ecstypes.DeploymentRolloutStateCompleted)}}
	if rolloutInProgress(svc) {
		t.Error("a single COMPLETED primary deployment is settled")
	}
	svc = &ecstypes.Service{Deployments: []ecstypes.Deployment{primary(ecstypes.DeploymentRolloutStateInProgress)}}
	if !rolloutInProgress(svc) {
		t.Error("an IN_PROGRESS primary deployment must defer")
	}
	svc = &ecstypes.Service{Deployments: []ecstypes.Deployment{
		primary(ecstypes.DeploymentRolloutStateCompleted),
		{Status: aws.String("ACTIVE"), RolloutState: ecstypes.DeploymentRolloutStateCompleted},
	}}
	if !rolloutInProgress(svc) {
		t.Error("an old deployment still draining (two deployments) must defer")
	}
}

func TestLiveDeploymentController(t *testing.T) {
	if got := liveDeploymentController(&ecstypes.Service{}); got != ecstypes.DeploymentControllerTypeEcs {
		t.Errorf("nil controller should default to ECS, got %q", got)
	}
	cd := &ecstypes.Service{DeploymentController: &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeCodeDeploy}}
	if got := liveDeploymentController(cd); got != ecstypes.DeploymentControllerTypeCodeDeploy {
		t.Errorf("got %q", got)
	}
}

func TestStandaloneUnsupportedMessageClassifies(t *testing.T) {
	// workloadreport.ToModel classifies any error carrying the unsupported prefix as a terminal
	// Unsupported state — the standalone-task message must ride that path.
	if !strings.HasPrefix(standaloneUnsupportedMsg, baseapi.ErrPrefixUnsupported) {
		t.Errorf("standalone message %q must start with %q", standaloneUnsupportedMsg, baseapi.ErrPrefixUnsupported)
	}
}

func TestWithPassRoleHint(t *testing.T) {
	denied := errors.New("api error AccessDeniedException: User is not authorized to perform iam:PassRole")
	if got := withPassRoleHint(denied); !strings.Contains(got.Error(), "iam:PassedToService") {
		t.Errorf("AccessDenied should gain the pass-role hint, got %v", got)
	}
	plain := errors.New("throttled")
	if got := withPassRoleHint(plain); got != plain {
		t.Errorf("non-AccessDenied errors must pass through, got %v", got)
	}
	if withPassRoleHint(nil) != nil {
		t.Error("nil must pass through")
	}
}

func TestTaskDefOSFamilyGateValues(t *testing.T) {
	if got := taskDefOSFamily(&ecstypes.TaskDefinition{}); got != "" {
		t.Errorf("no runtime platform should mean Linux (empty), got %q", got)
	}
	win := &ecstypes.TaskDefinition{RuntimePlatform: &ecstypes.RuntimePlatform{OperatingSystemFamily: ecstypes.OSFamilyWindowsServer2019Core}}
	if got := taskDefOSFamily(win); got == "" || got == string(ecstypes.OSFamilyLinux) {
		t.Errorf("windows family should be surfaced, got %q", got)
	}
}
