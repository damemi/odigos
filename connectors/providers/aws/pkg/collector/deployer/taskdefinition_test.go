package deployer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/opamp"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// testPin is an installation-selected digest, independent of any published image.
func testPin() collectorimage.Pin {
	return collectorimage.Pin{
		Digest: "sha256:" + strings.Repeat("a", 64),
		Tag:    "v1.2.3",
		Repositories: map[collectorimage.Cloud]string{
			collectorimage.CloudAWS: "ghcr.io/odigos-io/odigos-enterprise-collector",
		},
		Platforms: []collectorimage.Arch{collectorimage.ArchAMD64, collectorimage.ArchARM64},
	}
}

func taskDefinitionInput() TaskDefinitionInput {
	return TaskDefinitionInput{
		HostNetworkID:    testVPC,
		Region:           testRegion,
		Connector:        testConnector,
		ConnectorID:      testUID,
		Identity:         testIdentity(),
		Mode:             spec.ModeAutomatic,
		ExecutionRoleARN: "arn:aws:iam::" + testAccount + ":role/OdigosCollectorExecutionRole",
		LogGroup:         LogGroupName(testConnector),
		Delivery: collectorimage.Delivery{
			LicenseSecretRef:    "arn:aws:secretsmanager:" + testRegion + ":" + testAccount + ":secret:license-AbCdEf",
			CredentialSecretRef: "arn:aws:secretsmanager:" + testRegion + ":" + testAccount + ":secret:credential-AbCdEf",
		},
		OpAMPEndpoint: "wss://central.example.com/v1/opamp",
		Pin:           testPin(),
		CPU:           "1024",
		Memory:        "2048",
	}
}

func TestTaskDefinitionFamilyIsRegional(t *testing.T) {
	first := taskDefinitionInput()
	second := first
	second.HostNetworkID = "vpc-other-host"

	firstRendered, firstProblems := RenderTaskDefinition(first)
	if collectorstatus.HasError(firstProblems) {
		t.Fatalf("first render problems = %v", codes(firstProblems))
	}
	secondRendered, secondProblems := RenderTaskDefinition(second)
	if collectorstatus.HasError(secondProblems) {
		t.Fatalf("second render problems = %v", codes(secondProblems))
	}
	if firstRendered.Family != secondRendered.Family {
		t.Fatalf("same-region families differ by host VPC: %q and %q", firstRendered.Family, secondRendered.Family)
	}
	if want := TaskDefinitionFamily(testRegion); firstRendered.Family != want {
		t.Errorf("family = %q, want regional family %q", firstRendered.Family, want)
	}

	second.Region = "us-east-1"
	thirdRendered, thirdProblems := RenderTaskDefinition(second)
	if collectorstatus.HasError(thirdProblems) {
		t.Fatalf("third render problems = %v", codes(thirdProblems))
	}
	if thirdRendered.Family == firstRendered.Family {
		t.Errorf("different regions share task-definition family %q", thirdRendered.Family)
	}
}

// TestTheRenderedDefinitionMatchesTheAgentContract. Every one of these is something
// collector/managed/supervisor.yaml or the README requires, and each has a distinct failure: the wrong
// command runs the plain Collector instead of the Supervisor, a missing endpoint starts a Supervisor
// that dials nothing, and a secret delivered as a plain value is readable by anyone with
// ecs:DescribeTaskDefinition.
func TestTheRenderedDefinitionMatchesTheAgentContract(t *testing.T) {
	rendered, problems := RenderTaskDefinition(taskDefinitionInput())
	if collectorstatus.HasError(problems) {
		t.Fatalf("problems = %v", codes(problems))
	}
	if len(rendered.Containers) != 1 {
		t.Fatalf("containers = %d, want one", len(rendered.Containers))
	}
	c := rendered.Containers[0]

	// The image still defaults to /odigosotelcol, so managed mode is an explicit command override —
	// the binary and its config flag, which is how the README's command+args reads on ECS.
	if len(c.Command) != 2 || c.Command[0] != "/odigosopampsupervisor" ||
		!strings.HasPrefix(c.Command[1], "--config=") {
		t.Errorf("command = %v, want the Supervisor and its bundled config", c.Command)
	}
	if !strings.Contains(c.Image, "@sha256:") {
		t.Errorf("image = %q, want a digest-pinned reference: a tag is not an image identity", c.Image)
	}
	if !c.Essential {
		t.Error("the Collector container is not essential, so the task would stay up after it died")
	}

	// The four ports §5 lists, including 13134 — never exposed through the load balancer, but the
	// container listens on it.
	wantPorts := map[int]bool{
		collectoringress.PortOTLPGRPC: true, collectoringress.PortOTLPHTTP: true,
		collectoringress.PortHealth: true, collectoringress.PortSupervisorHealth: true,
	}
	if len(c.Ports) != len(wantPorts) {
		t.Errorf("ports = %v, want %v", c.Ports, wantPorts)
	}
	for _, p := range c.Ports {
		if !wantPorts[p] {
			t.Errorf("unexpected port %d", p)
		}
	}

	// Every ${…} in supervisor.yaml has to be populated, or the Supervisor starts against an empty
	// endpoint or reports an agent Odigos cannot identify.
	for name, want := range map[string]string{
		opamp.EnvEndpoint:       "wss://central.example.com/v1/opamp",
		"ODIGOS_CLOUD_PROVIDER": "aws",
		// The connector UID, not the name: the OpAMP server verifies the reported id against the binding
		// the credential was issued under, and a mismatch is a refused handshake.
		"ODIGOS_CLOUD_CONNECTOR_ID": testUID,
	} {
		if c.Environment[name] != want {
			t.Errorf("%s = %q, want %q", name, c.Environment[name], want)
		}
	}

	// Both secrets arrive as refs, from two separate secrets.
	if c.Secrets[collectorimage.EnvOpAMPAuthorization] == "" || c.Secrets[collectorimage.EnvOnPremToken] == "" {
		t.Fatalf("secrets = %v, want both refs", c.Secrets)
	}
	if c.Secrets[collectorimage.EnvOpAMPAuthorization] == c.Secrets[collectorimage.EnvOnPremToken] {
		t.Error("both secrets come from one ref; they have different issuers, lifetimes and revocation authorities")
	}
	for _, name := range collectorimage.SecretEnvVars() {
		if _, plain := c.Environment[name]; plain {
			t.Errorf("%s is a plain environment value; anyone with ecs:DescribeTaskDefinition can read it", name)
		}
	}

	// Fargate task shape.
	if rendered.NetworkMode != string(ecstypes.NetworkModeAwsvpc) {
		t.Errorf("network mode = %q, want awsvpc: a Fargate task has no other, and awsvpc is what gives it the ENI the §7.7 rules are written against", rendered.NetworkMode)
	}
	if len(rendered.RequiresCompatibilities) != 1 || rendered.RequiresCompatibilities[0] != "FARGATE" {
		t.Errorf("compatibilities = %v", rendered.RequiresCompatibilities)
	}
	if rendered.Architecture != "X86_64" || rendered.OperatingSystemFamily != "LINUX" {
		t.Errorf("runtime platform = %s/%s, want X86_64/LINUX", rendered.Architecture, rendered.OperatingSystemFamily)
	}
	if c.LogDriver != "awslogs" || c.LogOptions["awslogs-group"] != LogGroupName(testConnector) ||
		c.LogOptions["awslogs-region"] != testRegion {
		t.Errorf("log configuration = %s %v", c.LogDriver, c.LogOptions)
	}
}

// TestTheArchitectureContractIsHonoured — an acceptance criterion. The pin declares which platforms its
// manifest list carries, so an unsupported architecture is refused BEFORE any write rather than
// discovered by a task that will not place, whose error reads as a capacity problem.
func TestTheArchitectureContractIsHonoured(t *testing.T) {
	t.Run("arm64 opt-in", func(t *testing.T) {
		in := taskDefinitionInput()
		in.Arch = string(collectorimage.ArchARM64)
		rendered, problems := RenderTaskDefinition(in)
		if collectorstatus.HasError(problems) {
			t.Fatalf("problems = %v", codes(problems))
		}
		if rendered.Architecture != "ARM64" {
			t.Errorf("architecture = %q, want ARM64", rendered.Architecture)
		}
	})

	t.Run("default is amd64", func(t *testing.T) {
		rendered, _ := RenderTaskDefinition(taskDefinitionInput())
		if rendered.Architecture != "X86_64" {
			t.Errorf("architecture = %q; ARM64 Fargate is not available in every region, so it is an opt-in", rendered.Architecture)
		}
	})

	t.Run("an architecture the pin does not carry is refused", func(t *testing.T) {
		in := taskDefinitionInput()
		in.Pin.Platforms = []collectorimage.Arch{collectorimage.ArchAMD64}
		in.Arch = string(collectorimage.ArchARM64)
		_, problems := RenderTaskDefinition(in)
		if !collectorstatus.HasError(problems) {
			t.Fatal("no refusal for an architecture the manifest list does not contain")
		}
		if !hasCode(problems, ProblemImageUnavailable) {
			t.Errorf("problems = %v", codes(problems))
		}
	})

	t.Run("a misspelled architecture is refused", func(t *testing.T) {
		in := taskDefinitionInput()
		in.Arch = "x86_64"
		_, problems := RenderTaskDefinition(in)
		if !collectorstatus.HasError(problems) {
			t.Fatal("a typo in arch was accepted")
		}
		// The message has to say what would work, because the value came from configuration.
		var named bool
		for _, p := range problems {
			if strings.Contains(p.Message, "arm64") || strings.Contains(p.Message, "amd64") {
				named = true
			}
		}
		if !named {
			t.Errorf("no problem names the valid architectures: %v", problems)
		}
	})
}

// An installation without an image must fail closed rather than choose an implicit latest.
func TestNoPinnedImageRefusesRatherThanDeploying(t *testing.T) {
	in := taskDefinitionInput()
	in.Pin = collectorimage.Pin{}
	_, problems := RenderTaskDefinition(in)
	if !collectorstatus.HasError(problems) {
		t.Fatal("rendered a definition with no image pinned")
	}
	if !hasCode(problems, ProblemImageUnavailable) {
		t.Errorf("problems = %v", codes(problems))
	}
}

// TestARepositoryOverrideKeepsTheDigest. An override says where to pull the bytes from — a pull-through
// cache, a private mirror — and the bytes still have to be the ones this build was released against, or
// the coverage guard's promise that both modes get identical bytes is worthless.
func TestARepositoryOverrideKeepsTheDigest(t *testing.T) {
	in := taskDefinitionInput()
	in.ImageRepositoryOverride = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/mirror/collector"

	rendered, problems := RenderTaskDefinition(in)
	if collectorstatus.HasError(problems) {
		t.Fatalf("problems = %v", codes(problems))
	}
	want := in.ImageRepositoryOverride + "@" + in.Pin.Digest
	if rendered.Containers[0].Image != want {
		t.Errorf("image = %q, want %q", rendered.Containers[0].Image, want)
	}

	// Every region shares the installation selection; an override must not mutate it for others.
	shared := testPin()
	withOverride := taskDefinitionInput()
	withOverride.Pin = shared
	withOverride.ImageRepositoryOverride = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/mirror/collector"
	if _, problems := RenderTaskDefinition(withOverride); collectorstatus.HasError(problems) {
		t.Fatalf("problems = %v", codes(problems))
	}
	plain := taskDefinitionInput()
	plain.Pin = shared
	plainRendered, _ := RenderTaskDefinition(plain)
	if strings.Contains(plainRendered.Containers[0].Image, "mirror/collector") {
		t.Fatalf("the override leaked into a network that has none: %q", plainRendered.Containers[0].Image)
	}
	if shared.Repositories[collectorimage.CloudAWS] != testPin().Repositories[collectorimage.CloudAWS] {
		t.Errorf("the pin's repository map was mutated in place: %v", shared.Repositories)
	}
}

func TestRuntimeImageChangeChangesTaskDefinitionFingerprint(t *testing.T) {
	in := taskDefinitionInput()
	before, problems := RenderTaskDefinition(in)
	if collectorstatus.HasError(problems) {
		t.Fatal(problems)
	}
	in.Image = collectorimage.Selection{"registry": "mirror.example.com", "tag": "v2.0.0"}
	after, problems := RenderTaskDefinition(in)
	if collectorstatus.HasError(problems) {
		t.Fatal(problems)
	}
	if got := after.Containers[0].Image; got != "mirror.example.com/odigos-io/odigos-enterprise-collector:v2.0.0" {
		t.Fatalf("image = %q", got)
	}
	if before.Fingerprint() == after.Fingerprint() {
		t.Fatal("image change must register a new task definition")
	}
}

func TestPublicECRPullIsAnonymousButRuntimeLicenseIsStillDelivered(t *testing.T) {
	in := taskDefinitionInput()
	in.Image = collectorimage.Selection{"reference": "public.ecr.aws/example/collector:v2"}
	got, problems := RenderTaskDefinition(in)
	if collectorstatus.HasError(problems) {
		t.Fatal(problems)
	}
	container := got.Containers[0]
	if container.RepositoryCredentialsRef != "" {
		t.Fatal("public ECR must not receive Odigos registry credentials")
	}
	if container.Secrets[collectorimage.EnvOnPremToken] != in.Delivery.LicenseSecretRef+":password::" {
		t.Fatal("anonymous image pull must still deliver the runtime license")
	}
	if container.Secrets[collectorimage.EnvOpAMPAuthorization] != in.Delivery.CredentialSecretRef {
		t.Fatal("anonymous image pull lost OpAMP authentication")
	}
	registered := registerInput(got, nil)
	if registered.ContainerDefinitions[0].RepositoryCredentials != nil {
		t.Fatal("ECS API must omit repositoryCredentials for public ECR")
	}
}

func TestNoOpAMPEndpointRefuses(t *testing.T) {
	in := taskDefinitionInput()
	in.OpAMPEndpoint = ""
	_, problems := RenderTaskDefinition(in)
	if !collectorstatus.HasError(problems) {
		t.Fatal("rendered a definition with no OpAMP endpoint; the Supervisor would dial nothing")
	}
	var named bool
	for _, p := range problems {
		if strings.Contains(p.Message, opamp.EnvEndpoint) {
			named = true
		}
	}
	if !named {
		t.Errorf("no problem names %s: %v", opamp.EnvEndpoint, problems)
	}
}

func TestAMissingSecretRefRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*TaskDefinitionInput)
	}{
		{"no licence", func(in *TaskDefinitionInput) { in.Delivery.LicenseSecretRef = "" }},
		{"no credential", func(in *TaskDefinitionInput) { in.Delivery.CredentialSecretRef = "" }},
		{"one secret for both", func(in *TaskDefinitionInput) {
			in.Delivery.CredentialSecretRef = in.Delivery.LicenseSecretRef
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := taskDefinitionInput()
			tc.apply(&in)
			if _, problems := RenderTaskDefinition(in); !collectorstatus.HasError(problems) {
				t.Fatal("no refusal")
			}
		})
	}
}

// TestTheFargateSizeMatrixIsEnforced. RegisterTaskDefinition refuses an unsupported pair with "No
// Fargate configuration exists for given values", which names neither field nor a value that would
// work — and would be the whole diagnosis available on every pass for ever.
func TestTheFargateSizeMatrixIsEnforced(t *testing.T) {
	valid := [][2]string{
		{"256", "512"}, {"256", "1024"}, {"256", "2048"},
		{"512", "1024"}, {"512", "4096"},
		{"1024", "2048"}, {"1024", "8192"},
		{"2048", "4096"}, {"2048", "16384"},
		{"4096", "8192"}, {"4096", "30720"},
		// The step above 4096 CPU is not 1 GiB: 4 GiB at 8192 and 8 GiB at 16384.
		{"8192", "16384"}, {"8192", "20480"}, {"8192", "61440"},
		{"16384", "32768"}, {"16384", "40960"}, {"16384", "122880"},
		{"32768", "61440"}, {"32768", "122880"}, {"32768", "249856"},
	}
	for _, pair := range valid {
		if problems := checkFargateSize(testRegion, pair[0], pair[1]); len(problems) > 0 {
			t.Errorf("cpu %s / memory %s was refused: %v", pair[0], pair[1], problems[0].Message)
		}
	}

	invalid := [][2]string{
		{"256", "256"},     // below the minimum
		{"256", "1536"},    // 256's memory values are a list, not a range
		{"512", "512"},     // below the minimum for that CPU
		{"1024", "1536"},   // not a whole GiB step
		{"1024", "9216"},   // above the maximum for that CPU
		{"8192", "17408"},  // a 1 GiB step, but 8192 CPU moves in 4 GiB
		{"16384", "36864"}, // a 4 GiB step, but 16384 CPU moves in 8 GiB
		{"32768", "65536"}, // 32 vCPU accepts discrete memory sizes
		{"768", "2048"},    // not a Fargate CPU size at all
		{"1", "2"},
		{"", "2048"},
		{"1024", ""},
		{"1 vCPU", "2 GB"},
	}
	for _, pair := range invalid {
		problems := checkFargateSize(testRegion, pair[0], pair[1])
		if len(problems) == 0 {
			t.Errorf("cpu %q / memory %q was accepted", pair[0], pair[1])
			continue
		}
		if problems[0].Severity != collectorstatus.SeverityError {
			t.Errorf("cpu %q / memory %q is only %q", pair[0], pair[1], problems[0].Severity)
		}
	}

	// The three sizes §8.1 suggests must all be valid, or the documentation recommends a Collector that
	// cannot be registered.
	for _, pair := range [][2]string{{"512", "1024"}, {"1024", "2048"}, {"2048", "4096"}} {
		if problems := checkFargateSize(testRegion, pair[0], pair[1]); len(problems) > 0 {
			t.Errorf("§8.1's %s/%s size is refused: %v", pair[0], pair[1], problems[0].Message)
		}
	}
}

// TestRenderRefusesAnUnsupportedSize pins that the matrix is actually CONSULTED. The check above tests
// the function; this tests that rendering calls it — without which an unsupported pair reaches
// RegisterTaskDefinition and fails on every pass with an error that names no field.
func TestRenderRefusesAnUnsupportedSize(t *testing.T) {
	in := taskDefinitionInput()
	in.CPU, in.Memory = "1024", "1536"

	rendered, problems := RenderTaskDefinition(in)
	if !collectorstatus.HasError(problems) {
		t.Fatal("rendered a definition Fargate would refuse")
	}
	if !hasCode(problems, ProblemTaskSizeInvalid) {
		t.Fatalf("problems = %v", codes(problems))
	}
	if len(rendered.Containers) != 0 {
		t.Error("returned a spec alongside an error; a caller reading only one of the two would deploy it")
	}
}

// --- the converge ---

// TestARevisionRoundTripsToTheSameFingerprint is the test the whole material-change design rests on.
//
// Render, register, describe, decode, fingerprint: if the digest moves, the next pass registers another
// revision, and the pass after that another — every one of them a rolling replacement of the network's
// Collector, which on ephemeral storage loses its queue each time. Nothing about the resulting behaviour
// looks like a bug; the Collector is simply always mid-deployment.
func TestARevisionRoundTripsToTheSameFingerprint(t *testing.T) {
	api := newFakeECS()
	in := taskDefinitionInput()

	rendered, problems := RenderTaskDefinition(in)
	if collectorstatus.HasError(problems) {
		t.Fatalf("problems = %v", codes(problems))
	}

	first, err := EnsureTaskDefinition(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureTaskDefinition: %v", err)
	}
	if !first.Registered || !first.Ready() {
		t.Fatalf("first pass = %+v", first)
	}

	// The definition ECS handed back, decoded, must fingerprint to what was rendered — even though ECS
	// added a revision, a status, an ARN, compatibilities, requiresAttributes and two registration
	// stamps that were never sent.
	described := api.latest(first.Family)
	if described == nil {
		t.Fatal("nothing was registered")
	}
	if described.TaskRoleArn != nil {
		t.Errorf("task role = %q, want none: the Collector does not call AWS APIs", aws.ToString(described.TaskRoleArn))
	}
	if got, want := specFromDefinition(described).Fingerprint(), rendered.Fingerprint(); got != want {
		t.Fatalf("a registered definition does not round-trip:\n  described %s\n  rendered  %s", got, want)
	}
}

// TestASecondPassRegistersNothing — two acceptance criteria at once: revisions only on material change,
// and a steady state with no writes.
func TestASecondPassRegistersNothing(t *testing.T) {
	api := newFakeECS()
	in := taskDefinitionInput()
	first, err := EnsureTaskDefinition(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}

	for pass := 0; pass < 5; pass++ {
		api.calls = nil
		got, err := EnsureTaskDefinition(context.Background(), api, in)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if got.Registered {
			t.Fatalf("pass %d registered a revision, which rolls every task on the network", pass)
		}
		if got.ARN != first.ARN || got.Revision != first.Revision {
			t.Errorf("pass %d = revision %d (%s), want %d (%s)", pass, got.Revision, got.ARN, first.Revision, first.ARN)
		}
		for _, call := range api.calls {
			if strings.HasPrefix(call, "Register") {
				t.Errorf("pass %d called %s", pass, call)
			}
		}
	}
	if api.revisionCount(first.Family) != 1 {
		t.Errorf("the family holds %d revisions after six passes", api.revisionCount(first.Family))
	}
}

// TestEveryMaterialChangeRegistersARevision walks each field a customer or a release can change and
// asserts it produces one. A field the fingerprint forgot would be a configuration change that silently
// never reaches the Collector.
func TestEveryMaterialChangeRegistersARevision(t *testing.T) {
	changes := []struct {
		name  string
		apply func(*TaskDefinitionInput)
	}{
		{"image digest", func(in *TaskDefinitionInput) {
			in.Pin.Digest = "sha256:" + strings.Repeat("b", 64)
		}},
		{"repository override", func(in *TaskDefinitionInput) {
			in.ImageRepositoryOverride = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/mirror/collector"
		}},
		{"cpu", func(in *TaskDefinitionInput) { in.CPU, in.Memory = "2048", "4096" }},
		{"memory", func(in *TaskDefinitionInput) { in.Memory = "4096" }},
		{"architecture", func(in *TaskDefinitionInput) { in.Arch = string(collectorimage.ArchARM64) }},
		{"execution role", func(in *TaskDefinitionInput) { in.ExecutionRoleARN += "-2" }},
		{"log group", func(in *TaskDefinitionInput) { in.LogGroup += "-2" }},
		{"region", func(in *TaskDefinitionInput) { in.Region = "us-east-1" }},
		{"opamp endpoint", func(in *TaskDefinitionInput) { in.OpAMPEndpoint = "wss://other.example.com/v1/opamp" }},
		{"connector id", func(in *TaskDefinitionInput) { in.ConnectorID = "uid-2" }},
		// A rotated credential is a new secret VERSION at the same ARN, so it is deliberately NOT a
		// change here — but a new secret is.
		{"credential secret", func(in *TaskDefinitionInput) { in.Delivery.CredentialSecretRef += "-2" }},
		{"licence secret", func(in *TaskDefinitionInput) { in.Delivery.LicenseSecretRef += "-2" }},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeECS()
			in := taskDefinitionInput()
			if _, err := EnsureTaskDefinition(context.Background(), api, in); err != nil {
				t.Fatal(err)
			}

			changed := taskDefinitionInput()
			tc.apply(&changed)
			got, err := EnsureTaskDefinition(context.Background(), api, changed)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Registered {
				t.Fatalf("changing the %s did not register a revision, so the change never reaches the Collector", tc.name)
			}
			if got.Revision != 2 {
				t.Errorf("revision = %d, want 2", got.Revision)
			}

			// And the pass after the change settles again.
			settled, err := EnsureTaskDefinition(context.Background(), api, changed)
			if err != nil {
				t.Fatal(err)
			}
			if settled.Registered {
				t.Error("the pass after the change registered another revision")
			}
		})
	}
}

// TestASidecarSomebodyAddedRegistersOurShapeBack. A revision cannot be edited, so "somebody changed it"
// means somebody registered a different one into the family — and the family holds this network's
// Collector, so Odigos's shape is what belongs there.
func TestASidecarSomebodyAddedRegistersOurShapeBack(t *testing.T) {
	api := newFakeECS()
	in := taskDefinitionInput()
	if _, err := EnsureTaskDefinition(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}

	// A second revision with an extra container, tagged as ours so the ownership gate lets the converge
	// proceed — this is about the SHAPE, not about attribution.
	family := TaskDefinitionFamily(testRegion)
	edited := *api.latest(family)
	edited.ContainerDefinitions = append(edited.ContainerDefinitions, ecstypes.ContainerDefinition{
		Name:  aws.String("sidecar"),
		Image: aws.String("busybox@sha256:" + strings.Repeat("c", 64)),
	})
	edited.Revision = 2
	edited.TaskDefinitionArn = aws.String(aws.ToString(edited.TaskDefinitionArn) + "-edited")
	api.families[family] = append(api.families[family], edited)
	api.tags[aws.ToString(edited.TaskDefinitionArn)] = ourTags(testVPC)

	got, err := EnsureTaskDefinition(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Registered {
		t.Fatal("a revision with an extra container was accepted as equivalent")
	}
}

// TestAFamilyThatIsNotOursIsRefused. Registering into a customer's family would add a revision to
// something they own, and their service could then roll onto it.
func TestAFamilyThatIsNotOursIsRefused(t *testing.T) {
	api := newFakeECS()
	family := TaskDefinitionFamily(testRegion)
	api.families[family] = []ecstypes.TaskDefinition{{
		TaskDefinitionArn: aws.String("arn:aws:ecs:" + testRegion + ":" + testAccount + ":task-definition/" + family + ":1"),
		Family:            aws.String(family),
		Revision:          1,
		Status:            ecstypes.TaskDefinitionStatusActive,
	}}
	api.tags["arn:aws:ecs:"+testRegion+":"+testAccount+":task-definition/"+family+":1"] = map[string]string{"team": "platform"}

	got, err := EnsureTaskDefinition(context.Background(), api, taskDefinitionInput())
	if err != nil {
		t.Fatal(err)
	}
	if got.ARN != "" || got.Registered {
		t.Errorf("registered into a family Odigos does not own: %+v", got)
	}
	if !collectorstatus.HasError(got.Problems) || got.Problems[0].Code != ProblemObjectNotOurs {
		t.Fatalf("problems = %v", codes(got.Problems))
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "Register") {
			t.Errorf("wrote to a family Odigos does not own: %v", api.calls)
		}
	}
}

// TestAnInactiveFamilyIsRegisteredInto. Every revision deregistered leaves the family describable but
// INACTIVE — nothing can run it, so comparing against it would keep a Collector permanently undeployed.
func TestAnInactiveFamilyIsRegisteredInto(t *testing.T) {
	api := newFakeECS()
	family := TaskDefinitionFamily(testRegion)
	api.families[family] = []ecstypes.TaskDefinition{{
		TaskDefinitionArn: aws.String("arn:.../" + family + ":1"),
		Family:            aws.String(family),
		Revision:          1,
		Status:            ecstypes.TaskDefinitionStatusInactive,
	}}

	got, err := EnsureTaskDefinition(context.Background(), api, taskDefinitionInput())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Registered || !got.Ready() {
		t.Fatalf("an inactive family was not registered into: %+v", got)
	}
}

// TestTheRevisionIsTaggedAsOdigosCreated. The first half of the deletion gate (§10.4): teardown may
// deregister only what the tag says Odigos created.
func TestTheRevisionIsTaggedAsOdigosCreated(t *testing.T) {
	api := newFakeECS()
	got, err := EnsureTaskDefinition(context.Background(), api, taskDefinitionInput())
	if err != nil {
		t.Fatal(err)
	}
	parsed := baseapi.ParseCollectorTags(api.tags[got.ARN])
	if !parsed.IsCollector() || !parsed.HasOwnershipMarker() || parsed.Network != testVPC {
		t.Errorf("revision tags = %v", api.tags[got.ARN])
	}
}

func TestARegisterFailureIsAnError(t *testing.T) {
	api := newFakeECS()
	api.registerErr = errors.New("throttled")
	if _, err := EnsureTaskDefinition(context.Background(), api, taskDefinitionInput()); err == nil {
		t.Fatal("no error")
	}
}

func TestManualModeRegistersNothing(t *testing.T) {
	api := newFakeECS()
	in := taskDefinitionInput()
	in.Mode = spec.ModeManual

	got, err := EnsureTaskDefinition(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ARN != "" || len(api.calls) != 0 {
		t.Errorf("touched ECS in manual mode: %+v / %v", got, api.calls)
	}
	if len(got.Problems) != 1 || got.Problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", codes(got.Problems))
	}
}
