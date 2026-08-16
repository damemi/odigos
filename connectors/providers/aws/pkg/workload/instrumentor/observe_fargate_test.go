package instrumentor

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// instrumentedTaskDef returns a task definition instrumented exactly as desired for one java app container,
// plus the target set it was instrumented for — the converged baseline every case below perturbs.
func instrumentedTaskDef(t *testing.T) (*ecstypes.TaskDefinition, []containerTarget) {
	t.Helper()
	rec := testRecipe(t, LangJava, "")
	targets := oneTarget(0, rec, testExport())
	in, err := applyInstrumentation(sampleTaskDef(), targets, nil, testAgentsImage, "svc-checkout", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	td := tdFromRegisterInput(in)
	if len(containerDiff(&td.ContainerDefinitions[0], targets[0], "svc-checkout", "us-east-1", false)) != 0 ||
		len(taskLevelDiff(td, targets, testAgentsImage)) != 0 {
		t.Fatal("test setup: a freshly instrumented task definition must be conformant, or every case below is vacuous")
	}
	return td, targets
}

// TestContainerDiffNamesWhatIsWrong is the heart of the Fargate observer. In manual mode nobody converges a
// gap — the customer edits their own task definition — so the component list IS the remedy they receive, and
// each case asserts the EXACT set because a wrong component name sends someone to change the wrong thing.
func TestContainerDiffNamesWhatIsWrong(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(app *ecstypes.ContainerDefinition)
		want   []string
	}{
		{
			name:   "instrumented exactly as desired",
			mutate: func(*ecstypes.ContainerDefinition) {},
			want:   nil,
		},
		{
			// No marker: nothing on this container is attributable to Odigos. It must report ONLY the marker —
			// listing the rest would describe an uninstrumented container, or a customer's own OpenTelemetry
			// setup, as broken Odigos instrumentation.
			name: "no ownership marker",
			mutate: func(app *ecstypes.ContainerDefinition) {
				deleteContainerEnv(app, store.EnvOdigosManaged)
			},
			want: []string{componentOwnershipMarker},
		},
		{
			// The ECS analog of Lambda's detached layer at container scope: the agent files are staged into a
			// volume this container no longer mounts, so the activation env points at an empty path and the
			// application exports nothing while reporting as instrumented.
			name:   "shared volume no longer mounted",
			mutate: removeAppMount,
			want:   []string{componentAgentVolumeMount},
		},
		{
			// Without the dependency the app container can start before the copier has finished staging, so the
			// agent is missing on a cold start and present later — the worst kind of intermittent.
			name: "startup dependency on the copier dropped",
			mutate: func(app *ecstypes.ContainerDefinition) {
				removeDependsOn(app, sidecarContainerName)
			},
			want: []string{componentAgentStartupDependency},
		},
		{
			name: "service name renamed on the source",
			mutate: func(app *ecstypes.ContainerDefinition) {
				setContainerEnv(app, EnvServiceName, "old-name")
			},
			want: []string{componentServiceName},
		},
		{
			name: "destination endpoint changed",
			mutate: func(app *ecstypes.ContainerDefinition) {
				setContainerEnv(app, envOTLPEndpoint, "http://old-collector:4317")
			},
			want: []string{componentCollectorExport},
		},
		{
			// The destination gained a signal: the exporter selectors are stale even though the endpoint
			// matches, so telemetry the backend now accepts is still suppressed.
			name: "destination gained a signal",
			mutate: func(app *ecstypes.ContainerDefinition) {
				setContainerEnv(app, EnvMetricsExporter, exporterOTLP)
			},
			want: []string{componentCollectorExport},
		},
		{
			name: "resource attributes stale",
			mutate: func(app *ecstypes.ContainerDefinition) {
				setContainerEnv(app, envResourceAttributes, "cloud.provider=aws")
			},
			want: []string{componentResourceAttributes},
		},
		{
			// The agent files are staged and mounted, and nothing loads them.
			name: "agent activation env stripped",
			mutate: func(app *ecstypes.ContainerDefinition) {
				setContainerEnv(app, EnvJavaToolOptions, "-Xmx512m")
			},
			want: []string{componentAgentLoader},
		},
		{
			// The customer swapped the image for another language. The staged agent is for the old one, which
			// the env comparison alone would not always catch.
			name: "marker records a different language",
			mutate: func(app *ecstypes.ContainerDefinition) {
				m, _ := parseECSManifest(mustEnv(app, store.EnvOdigosManaged))
				m.Language = LangPython
				raw, err := marshalECSManifest(m)
				if err != nil {
					t.Fatal(err)
				}
				setContainerEnv(app, store.EnvOdigosManaged, raw)
			},
			want: []string{componentAgentLanguage},
		},
		{
			name: "debug log level expected but absent",
			mutate: func(app *ecstypes.ContainerDefinition) {
				setContainerEnv(app, EnvOtelLogLevel, "info")
			},
			want: []string{componentLogLevel},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			td, targets := instrumentedTaskDef(t)
			c.mutate(&td.ContainerDefinitions[0])

			got := containerDiff(&td.ContainerDefinitions[0], targets[0], "svc-checkout", "us-east-1", false)
			if !sameSet(got, c.want) {
				t.Errorf("diff = %v, want %v", got, c.want)
			}
			// The mutating path's predicate must stay exactly "every diff is empty", so observation and
			// instrumentation can never hold two definitions of "correctly instrumented".
			converged := alreadyInstrumented(td, targets, testAgentsImage, "svc-checkout", "us-east-1", false)
			if converged != (len(got) == 0) {
				t.Errorf("alreadyInstrumented = %v but container diff = %v; the two must agree", converged, got)
			}
		})
	}
}

// TestTaskLevelDiffCoversAgentStaging is the false green this pass exists to close, at task scope: every
// activation env var can be perfect while the thing that puts the agent on disk is gone. The application then
// starts, finds nothing at /var/odigos, and exports no telemetry at all — reporting as instrumented.
func TestTaskLevelDiffCoversAgentStaging(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(td *ecstypes.TaskDefinition)
		want   []string
	}{
		{
			name:   "fully staged",
			mutate: func(*ecstypes.TaskDefinition) {},
			want:   nil,
		},
		{
			name:   "copier container removed",
			mutate: func(td *ecstypes.TaskDefinition) { removeECSContainer(td, sidecarContainerName) },
			want:   []string{componentAgentSidecar},
		},
		{
			name:   "shared volume removed",
			mutate: removeAgentVolume,
			want:   []string{componentAgentVolume},
		},
		{
			// A stale agents image stages a different agent build than the activation env expects.
			name: "copier image changed",
			mutate: func(td *ecstypes.TaskDefinition) {
				findECSContainer(td, sidecarContainerName).Image = aws.String("someone-elses:image")
			},
			want: []string{componentAgentSidecar},
		},
		{
			// An ESSENTIAL copier fails the whole task the moment it exits 0, which is its entire job.
			name: "copier marked essential",
			mutate: func(td *ecstypes.TaskDefinition) {
				findECSContainer(td, sidecarContainerName).Essential = aws.Bool(true)
			},
			want: []string{componentAgentSidecar},
		},
		{
			// A copy command missing this language's directory stages nothing for it.
			name: "copier command changed",
			mutate: func(td *ecstypes.TaskDefinition) {
				findECSContainer(td, sidecarContainerName).Command = []string{"sh", "-c", "true"}
			},
			want: []string{componentAgentSidecar},
		},
		{
			name: "both the copier and the volume gone",
			mutate: func(td *ecstypes.TaskDefinition) {
				removeECSContainer(td, sidecarContainerName)
				removeAgentVolume(td)
			},
			want: []string{componentAgentSidecar, componentAgentVolume},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			td, targets := instrumentedTaskDef(t)
			c.mutate(td)
			if got := taskLevelDiff(td, targets, testAgentsImage); !sameSet(got, c.want) {
				t.Errorf("diff = %v, want %v", got, c.want)
			}
		})
	}
}

// TestEvaluateJudgesOwnedContainersOnly is the decision that separates manual-mode drift from a coverage
// opinion, and it is where the observer deliberately departs from the mutating path.
//
// alreadyInstrumented requires the owned set to EQUAL the target set, because for it a mismatch means
// "converge it" at the cost of one revision. Here the same mismatch would mean telling a customer their
// deployment has drifted because Odigos detected a container they chose not to instrument — resting on
// language detection whose image-config layer the manual-mode policy does not even grant. Such a container is
// still surfaced, in the container inventory, with its detected language and eligibility.
func TestEvaluateJudgesOwnedContainersOnly(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	td, targets := instrumentedTaskDef(t)

	// A second, uninstrumented app container that detection would happily target.
	td.ContainerDefinitions = append(td.ContainerDefinitions, ecstypes.ContainerDefinition{
		Name:      aws.String("sidecar-app"),
		Image:     aws.String("openjdk:21"),
		Essential: aws.Bool(true),
	})
	extra := containerTarget{idx: len(td.ContainerDefinitions) - 1, name: "sidecar-app", recipe: rec, export: testExport()}
	withBoth := append(append([]containerTarget{}, targets...), extra)

	svc := store.ECSService{ClusterARN: "arn:cluster", ServiceName: "checkout", Region: "us-east-1"}
	src := enabledFargateSource{serviceName: "svc-checkout"}

	ev := evaluateECSObservation(td, withBoth, nil, src, svc, testAgentsImage, false)
	if ev.obs.Conformance != baseapi.ConformanceConformant {
		t.Errorf("conformance = %q (missing %v), want Conformant: a container the customer did not instrument is a coverage gap, not drift",
			ev.obs.Conformance, ev.obs.Missing)
	}
	if !ev.owned {
		t.Error("owned = false, want true")
	}

	// The mutating path must still see the same task definition as NOT converged — it has a container to
	// instrument. The two answers differing is the point, not a bug.
	if alreadyInstrumented(td, withBoth, testAgentsImage, "svc-checkout", "us-east-1", false) {
		t.Error("the mutating path must still want to instrument the second container")
	}

	// An OWNED container whose target cannot be resolved is a different situation: there is no desired state
	// to compare against, so the check is INCOMPLETE rather than drifted. Saying "drifted" would be a guess
	// about a container whose expected wiring we could not build.
	ev = evaluateECSObservation(td, []containerTarget{extra}, nil, src, svc, testAgentsImage, false)
	if ev.obs.Error == "" {
		t.Error("an owned container with no resolvable target must report an incomplete check")
	}
	if ev.obs.Conformance != baseapi.ConformanceUnknown {
		t.Errorf("conformance = %q, want Unknown so the caller preserves the last confirmed verdict", ev.obs.Conformance)
	}
}

// TestEvaluateBranches walks the situations one settled read can be in. The load-bearing distinctions are
// between "nothing applied" (awaiting the customer), "applied and wrong" (drift), "cannot be instrumented at
// all" (unsupported) and "could not check" (incomplete) — reporting any of these as another is the failure
// mode manual mode is most exposed to, since nobody is converging anything behind the report.
func TestEvaluateBranches(t *testing.T) {
	svc := store.ECSService{ClusterARN: "arn:cluster", ServiceName: "checkout", Region: "us-east-1"}
	src := enabledFargateSource{serviceName: "svc-checkout"}

	t.Run("nothing applied", func(t *testing.T) {
		rec := testRecipe(t, LangJava, "")
		td := sampleTaskDef()
		ev := evaluateECSObservation(td, oneTarget(0, rec, testExport()), nil, src, svc, testAgentsImage, false)
		if ev.obs.Conformance != baseapi.ConformanceAbsent || ev.owned || ev.terminal != "" {
			t.Errorf("got (%q, owned=%v, terminal=%q), want (Absent, false, no terminal)", ev.obs.Conformance, ev.owned, ev.terminal)
		}
	})

	t.Run("nothing applied and nothing instrumentable", func(t *testing.T) {
		// The selection failure explains why awaiting-the-customer would never resolve on its own, so it is
		// recorded as a terminal condition with the remedy in its message.
		td := sampleTaskDef()
		ev := evaluateECSObservation(td, nil, errors.New("unsupported: no container could be instrumented"), src, svc, testAgentsImage, false)
		if ev.obs.Conformance != baseapi.ConformanceAbsent {
			t.Errorf("conformance = %q, want Absent", ev.obs.Conformance)
		}
		if ev.terminal == "" {
			t.Error("a selection failure with nothing applied must be recorded, or the customer waits forever with no explanation")
		}
	})

	t.Run("applied but the desired state cannot be recomputed", func(t *testing.T) {
		// php/ruby whose major.minor came from an image-config read manual mode does not grant. Instrumentation
		// IS applied, so this must NOT become drift.
		td, targets := instrumentedTaskDef(t)
		ev := evaluateECSObservation(td, nil, errors.New("needs runtime version"), src, svc, testAgentsImage, false)
		if ev.obs.Error == "" {
			t.Fatal("want an incomplete check")
		}
		if ev.obs.Conformance != baseapi.ConformanceUnknown || len(ev.obs.Missing) != 0 {
			t.Errorf("got (%q, %v), want Unknown with no components so the caller preserves the last verdict",
				ev.obs.Conformance, ev.obs.Missing)
		}
		if ev.terminal != "" {
			t.Error("an applied-but-unverifiable service must not be declared unsupported")
		}
		_ = targets
	})

	t.Run("applied and drifted", func(t *testing.T) {
		td, targets := instrumentedTaskDef(t)
		removeECSContainer(td, sidecarContainerName)
		ev := evaluateECSObservation(td, targets, nil, src, svc, testAgentsImage, false)
		if ev.obs.Conformance != baseapi.ConformancePartial || !sameSet(ev.obs.Missing, []string{componentAgentSidecar}) {
			t.Errorf("got (%q, %v), want Partial missing the copier", ev.obs.Conformance, ev.obs.Missing)
		}
		if !ev.owned {
			t.Error("owned = false, want true: the marker is still there, which is exactly why this is drift and not absence")
		}
	})

	t.Run("windows task definition is terminal in both modes", func(t *testing.T) {
		td, targets := instrumentedTaskDef(t)
		td.RuntimePlatform = &ecstypes.RuntimePlatform{OperatingSystemFamily: ecstypes.OSFamilyWindowsServer2019Full}
		ev := evaluateECSObservation(td, targets, nil, src, svc, testAgentsImage, false)
		if ev.terminal == "" {
			t.Error("the agents bundle is Linux-only, so a Windows task definition must be reported unsupported")
		}
		if ev.obs.Conformance != baseapi.ConformanceAbsent {
			t.Errorf("conformance = %q, want Absent", ev.obs.Conformance)
		}
	})

	t.Run("owned service reports the flag even when unsupported", func(t *testing.T) {
		// The instrumented flag is what drives the report's "there is live instrumentation to remove" branch,
		// so it must be reported from the markers regardless of any terminal condition found alongside it.
		td, targets := instrumentedTaskDef(t)
		td.RuntimePlatform = &ecstypes.RuntimePlatform{OperatingSystemFamily: ecstypes.OSFamilyWindowsServer2019Full}
		if ev := evaluateECSObservation(td, targets, nil, src, svc, testAgentsImage, false); !ev.owned {
			t.Error("owned = false, want true: the markers are on the task definition either way")
		}
	})
}

// TestDeploymentControllerIsNotAManualModeExclusion is the substantive difference between the two modes, and
// the one this ticket most needed to get right.
//
// CODE_DEPLOY and EXTERNAL block UpdateService from changing a task definition — they block ODIGOS's rollout,
// not instrumentation itself. A customer running blue/green deploys instruments the task definition in their
// own pipeline and it works perfectly. Inheriting the automatic-mode exclusion would report their correctly
// instrumented service as unsupported and tell them to stop using CodeDeploy.
func TestDeploymentControllerIsNotAManualModeExclusion(t *testing.T) {
	svc := store.ECSService{
		ClusterARN: "arn:cluster", ServiceName: "checkout", Region: "us-east-1",
		DeploymentController: string(ecstypes.DeploymentControllerTypeCodeDeploy),
	}
	td, targets := instrumentedTaskDef(t)

	ev := evaluateECSObservation(td, targets, nil, enabledFargateSource{serviceName: "svc-checkout"}, svc, testAgentsImage, false)
	if ev.terminal != "" {
		t.Errorf("terminal = %q; a CodeDeploy service the CUSTOMER instruments is not unsupported", ev.terminal)
	}
	if ev.obs.Conformance != baseapi.ConformanceConformant {
		t.Errorf("conformance = %q, want Conformant", ev.obs.Conformance)
	}
}

// TestOwnedDiffDeduplicatesAcrossContainers: two owned containers broken the same way must not name the same
// component twice. The list is customer-facing prose, and "the shared volume is missing; the shared volume is
// missing" reads as a bug in Odigos rather than a fact about their deployment.
func TestOwnedDiffDeduplicatesAcrossContainers(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	base := sampleTaskDef()
	base.ContainerDefinitions = append(base.ContainerDefinitions, ecstypes.ContainerDefinition{
		Name:      aws.String("worker"),
		Image:     aws.String("openjdk:21"),
		Essential: aws.Bool(true),
	})
	targets := []containerTarget{
		{idx: 0, name: "app", recipe: rec, export: testExport()},
		{idx: 1, name: "worker", recipe: rec, export: testExport()},
	}
	in, err := applyInstrumentation(base, targets, nil, testAgentsImage, "svc-checkout", "us-east-1", false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	td := tdFromRegisterInput(in)
	// Break both containers identically.
	removeAppMount(&td.ContainerDefinitions[0])
	removeAppMount(&td.ContainerDefinitions[1])

	svc := store.ECSService{ClusterARN: "arn:cluster", ServiceName: "checkout", Region: "us-east-1"}
	diff, incomplete := ecsOwnedDiff(td, ownedContainerIndexes(td), targets, "svc-checkout", svc, testAgentsImage, false)
	if incomplete != "" {
		t.Fatalf("unexpected incomplete check: %s", incomplete)
	}
	if !sameSet(diff, []string{componentAgentVolumeMount}) {
		t.Errorf("diff = %v, want the component named once", diff)
	}
}

// TestSameComponentsDetectsChange guards the write-skip. If it reported two different component lists as equal,
// a service whose drift changed shape would keep reporting the old components — and the customer would fix the
// wrong thing.
func TestSameComponentsDetectsChange(t *testing.T) {
	cases := []struct {
		a, b []string
		same bool
	}{
		{nil, nil, true},
		{[]string{"a"}, []string{"a"}, true},
		{nil, []string{"a"}, false},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a"}, []string{"a", "b"}, false},
		{[]string{"a", "b"}, []string{"b", "a"}, false}, // the diff order is stable, so a reorder is a real change
	}
	for _, c := range cases {
		if got := sameComponents(c.a, c.b); got != c.same {
			t.Errorf("sameComponents(%v, %v) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}

func mustEnv(c *ecstypes.ContainerDefinition, name string) string {
	v, _ := containerEnvValue(c, name)
	return v
}
