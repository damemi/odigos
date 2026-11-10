package instrumentor

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

// The agents-bundle image a manually instrumented task definition is held to.
//
// The failure this file exists for has no symptom at the workload: the customer's task definition is exactly
// what their artifact says, it stages agents correctly, telemetry flows — and the moment ARTIFACTS_VERSION is
// bumped in this repository every one of those services is reported as drifted on the agent-staging
// component, for a change made in Odigos rather than in their account. Lambda already refuses to do that
// (recordedLayer / desiredLayerARN); these are the same rules on the Fargate side.

// Synthetic tags, deliberately not real release numbers: what these cases turn on is that the image the
// customer APPLIED differs from the one this connector build pins, and pinning the fixtures to whatever
// ARTIFACTS_VERSION happens to say today would make an ordinary version bump edit this file.
const (
	iacImageApplied      = "registry.odigos.io/odigos-fargate-agents:as-applied"
	iacImageConnectorPin = "registry.odigos.io/odigos-fargate-agents:connector-pin"
)

// iacMarked returns a task definition whose single app container carries an IaC marker — the manual-mode
// shape, where no ODIGOS_MANAGED exists anywhere — staged from the given image ("" records none).
func iacMarked(t *testing.T, image string) (*ecstypes.TaskDefinition, []containerTarget) {
	t.Helper()
	rec := testRecipe(t, LangJava, "")
	targets := oneTarget(0, rec, testExport())
	in, err := applyInstrumentation(sampleTaskDef(), targets, nil, image, "svc-checkout", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	td := tdFromRegisterInput(in)

	// Swap the connector's ownership marker for the customer's IaC one: applyInstrumentation is the only
	// thing that builds a correct instrumented task definition, but what it produces is an OWNED one, and
	// every rule here turns on the task definition being the customer's instead.
	marker := baseapi.IaCMarker{Version: baseapi.IaCMarkerVersion, Source: "cdk", Language: LangJava, Image: image}
	payload, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	deleteContainerEnv(&td.ContainerDefinitions[0], store.EnvOdigosManaged)
	setContainerEnv(&td.ContainerDefinitions[0], store.EnvOdigosIaC, string(payload))
	return td, targets
}

func TestDesiredAgentsImageHonoursWhatTheIaCMarkerRecorded(t *testing.T) {
	td, targets := iacMarked(t, iacImageApplied)
	instrumented := instrumentedContainerIndexes(td)

	// The connector has moved on to a newer bundle; the customer's deployment has not, and says so.
	want := desiredAgentsImage(td, instrumented, iacImageConnectorPin)
	if !want.pinned || want.image != iacImageApplied {
		t.Fatalf("desired image = %+v, want the recorded %q", want, iacImageApplied)
	}
	if diff := taskLevelDiff(td, targets, want); len(diff) != 0 {
		t.Errorf("a task definition staging exactly the image it recorded is reported as %v; bumping "+
			"ARTIFACTS_VERSION must not drift every manually instrumented service in the account", diff)
	}
}

func TestDesiredAgentsImageStillCatchesAStaleStagingContainer(t *testing.T) {
	td, targets := iacMarked(t, iacImageApplied)
	// The customer's own definition says one image and the staging container carries another — the drift the
	// exact comparison exists to catch, and the recorded image is what makes it detectable rather than a
	// matter of this connector's opinion.
	findECSContainer(td, sidecarContainerName).Image = aws.String(iacImageConnectorPin)

	want := desiredAgentsImage(td, instrumentedContainerIndexes(td), iacImageConnectorPin)
	if !sameSet(taskLevelDiff(td, targets, want), []string{componentAgentSidecar}) {
		t.Error("a staging container running an image the marker did not record is not reported")
	}
}

func TestDesiredAgentsImageUnpinnedFallsBackToSignature(t *testing.T) {
	// A hand-authored marker — the Terraform and CloudFormation artifacts tell customers to write
	// {"v":1,"lang":"java"} and nothing more — records no image at all.
	td, targets := iacMarked(t, "")
	sc := findECSContainer(td, sidecarContainerName)
	sc.Image = aws.String("111122223333.dkr.ecr.us-east-1.amazonaws.com/my-mirror:dev")

	want := desiredAgentsImage(td, instrumentedContainerIndexes(td), iacImageConnectorPin)
	if want.pinned || want.conflict {
		t.Fatalf("desired image = %+v, want unpinned when nothing recorded one", want)
	}
	if diff := taskLevelDiff(td, targets, want); len(diff) != 0 {
		t.Errorf("diff = %v; with no recorded image the staging container is judged by signature, not "+
			"against this build's pin — a mirrored image is a documented, supported choice", diff)
	}

	// Signature still means signature: an essential stager is broken however it is tagged.
	sc.Essential = aws.Bool(true)
	if !sameSet(taskLevelDiff(td, targets, want), []string{componentAgentSidecar}) {
		t.Error("an unpinned image must not relax the non-essentiality check")
	}
}

func TestDesiredAgentsImageReportsAConflictBetweenContainers(t *testing.T) {
	td, targets := iacMarked(t, iacImageApplied)
	// A second application container, marked from a different release. One staging container cannot serve
	// both, so this is the customer's own inconsistency and has to be surfaced rather than resolved by
	// picking whichever marker was read first.
	second := ecstypes.ContainerDefinition{
		Name:      aws.String("worker"),
		Image:     aws.String("111122223333.dkr.ecr.us-east-1.amazonaws.com/worker:latest"),
		Essential: aws.Bool(true),
	}
	marker := baseapi.IaCMarker{Version: baseapi.IaCMarkerVersion, Language: LangPython, Image: iacImageConnectorPin}
	payload, _ := json.Marshal(marker)
	setContainerEnv(&second, store.EnvOdigosIaC, string(payload))
	td.ContainerDefinitions = append(td.ContainerDefinitions, second)

	want := desiredAgentsImage(td, instrumentedContainerIndexes(td), iacImageConnectorPin)
	if !want.conflict {
		t.Fatalf("desired image = %+v, want a conflict when two containers record different bundles", want)
	}
	// Everything else about the staging container is right — it is the disagreement itself that is reported,
	// on the component a customer would go and look at.
	if !sameSet(taskLevelDiff(td, targets, want), []string{componentAgentSidecar}) {
		t.Error("a recorded-image conflict is not reported on the staging component")
	}
}

// TestDesiredAgentsImageIgnoresRecordedImagesOnAnOwnedTaskDefinition pins the ownership half of the rule.
// When the connector may mutate a task definition it converges the staging container to its OWN configured
// image; honouring a value left over from an earlier manual application would leave it permanently unable to
// reach its desired state, reconciling every cycle and never converging.
func TestDesiredAgentsImageIgnoresRecordedImagesOnAnOwnedTaskDefinition(t *testing.T) {
	td, _ := iacMarked(t, iacImageApplied)
	// The customer hands the service to Odigos: the ownership marker joins the IaC one already there.
	setContainerEnv(&td.ContainerDefinitions[0], store.EnvOdigosManaged,
		`{"v":1,"lang":"java","prevEnv":{"JAVA_TOOL_OPTIONS":"-Xmx512m"}}`)

	want := desiredAgentsImage(td, instrumentedContainerIndexes(td), iacImageConnectorPin)
	if !want.pinned || want.image != iacImageConnectorPin {
		t.Errorf("desired image = %+v, want this connector's pin %q on a task definition it owns", want, iacImageConnectorPin)
	}
}

// TestRollbackKeepsWiringACustomerStillNeeds covers the takeover-then-release path, where the two markers
// coexist: a customer instrumented the task definition themselves, later handed the service to Odigos, and
// then turned automatic mode back off.
//
// Rolling back has to undo what the CONNECTOR did and nothing else. The staging container, the volume, the
// mount and the startup dependency were all already there — ensureAgentVolume, ensureSidecar, ensureAppMount
// and ensureDependsOn are idempotent, so the snapshot records no change to restore for any of them — and
// removing them regardless leaves the customer's own instrumentation pointing at agent files nothing stages.
// The container would keep its activation environment, keep reporting as instrumented, and export nothing.
func TestRollbackKeepsWiringACustomerStillNeeds(t *testing.T) {
	td, _ := iacMarked(t, iacImageApplied)
	setContainerEnv(&td.ContainerDefinitions[0], store.EnvOdigosManaged,
		`{"v":1,"lang":"java","prevEnv":{"JAVA_TOOL_OPTIONS":"-Xmx512m"}}`)

	in, ok := rollbackInstrumentation(td, nil)
	if !ok {
		t.Fatal("rollback refused a task definition carrying the ownership marker")
	}
	out := tdFromRegisterInput(in)

	if findECSContainer(out, sidecarContainerName) == nil {
		t.Error("the staging container was removed while a container still carries the customer's IaC marker, " +
			"leaving their instrumentation with no agent files on disk")
	}
	if !hasAgentVolume(out) {
		t.Error("the shared volume was removed while a container still needs it")
	}
	app := &out.ContainerDefinitions[0]
	if !containerHasMount(app) {
		t.Error("the customer's own volume mount was removed")
	}
	if !containerDependsOnSidecar(app) {
		t.Error("the customer's own startup dependency was removed")
	}
	// What rollback DOES undo: the connector's ownership marker and the env it snapshotted.
	if _, owned := containerEnvValue(app, store.EnvOdigosManaged); owned {
		t.Error("the ownership marker survived rollback")
	}
	if v, _ := containerEnvValue(app, "JAVA_TOOL_OPTIONS"); v != "-Xmx512m" {
		t.Errorf("JAVA_TOOL_OPTIONS = %q, want the snapshotted value restored", v)
	}
}

// TestRollbackOfAWhollyOwnedTaskDefinitionRemovesEverything is the other half: with no surviving marker there
// is nothing left relying on the staging container, so it and the volume go — a rolled-back service must not
// keep paying to pull an agents bundle nothing reads.
func TestRollbackOfAWhollyOwnedTaskDefinitionRemovesEverything(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	targets := oneTarget(0, rec, testExport())
	in, err := applyInstrumentation(sampleTaskDef(), targets, nil, iacImageConnectorPin, "svc-checkout", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	back, ok := rollbackInstrumentation(tdFromRegisterInput(in), nil)
	if !ok {
		t.Fatal("rollback refused a task definition the connector owns")
	}
	out := tdFromRegisterInput(back)

	if findECSContainer(out, sidecarContainerName) != nil {
		t.Error("the staging container survived a rollback with no marker left to justify it")
	}
	if hasAgentVolume(out) {
		t.Error("the shared volume survived a rollback with no marker left to justify it")
	}
	app := &out.ContainerDefinitions[0]
	if containerHasMount(app) || containerDependsOnSidecar(app) {
		t.Error("the mount or startup dependency the connector added survived rollback")
	}
	if v, _ := containerEnvValue(app, "JAVA_TOOL_OPTIONS"); v != "-Xmx512m" {
		t.Errorf("JAVA_TOOL_OPTIONS = %q, want the customer's original value", v)
	}
}
