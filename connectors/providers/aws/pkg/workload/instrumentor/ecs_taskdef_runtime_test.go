package instrumentor

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// container builds one container definition from a name, image and env pairs, for the task definitions below.
func container(name, image string, essential bool, env ...string) ecstypes.ContainerDefinition {
	c := ecstypes.ContainerDefinition{Name: aws.String(name), Image: aws.String(image), Essential: aws.Bool(essential)}
	for i := 0; i+1 < len(env); i += 2 {
		c.Environment = append(c.Environment, ecstypes.KeyValuePair{Name: aws.String(env[i]), Value: aws.String(env[i+1])})
	}
	return c
}

func taskDefOf(containers ...ecstypes.ContainerDefinition) *ecstypes.TaskDefinition {
	return &ecstypes.TaskDefinition{Family: aws.String("checkout"), ContainerDefinitions: containers}
}

// TestTaskDefinitionRuntimeReadsWhatTheInstrumentationRecorded is the case this resolution exists for: a
// Fargate service that is ALREADY instrumented — adopted from live instrumentation, so Odigos never chose the
// agent itself — must report the language the instrumentation on it records, not "unknown". Nothing about that
// answer is a guess: whoever applied the instrumentation wrote the language into the marker.
func TestTaskDefinitionRuntimeReadsWhatTheInstrumentationRecorded(t *testing.T) {
	cases := []struct {
		name    string
		marker  string
		value   string
		wantLan string
	}{
		{
			// Manual (IaC) mode — the customer's own CDK/Terraform wrote it. This is the exact shape
			// fargateIaCMarkerValue emits.
			name:    "iac marker",
			marker:  store.EnvOdigosIaC,
			value:   `{"v":1,"lang":"java"}`,
			wantLan: LangJava,
		},
		{
			// Automatic mode — the connector's own ownership marker, which carries the same fact plus the
			// rollback snapshot.
			name:    "ownership marker",
			marker:  store.EnvOdigosManaged,
			value:   `{"v":1,"lang":"python","prevEnv":{}}`,
			wantLan: LangPython,
		},
		{
			// A hand-authored marker with no language recorded is explicitly legitimate (see api.IaCMarker),
			// so it must fall through to detection rather than being treated as "language: none". The image
			// reference is the only signal here.
			name:    "marker without a recorded language falls through to detection",
			marker:  store.EnvOdigosIaC,
			value:   `{"v":1}`,
			wantLan: LangNodeJS,
		},
		{
			// A malformed marker is a customer typo, not a reason to report nothing — same rule
			// api.ParseIaCMarker applies one layer down.
			name:    "malformed marker falls through to detection",
			marker:  store.EnvOdigosIaC,
			value:   `{"v":`,
			wantLan: LangNodeJS,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			td := taskDefOf(container("app", "registry.example.com/checkout-node:1.4", true, tc.marker, tc.value))
			lang, _ := taskDefinitionRuntime(context.Background(), td)
			if lang != tc.wantLan {
				t.Fatalf("language = %q, want %q", lang, tc.wantLan)
			}
		})
	}
}

// TestTaskDefinitionRuntimeMakesNoRegistryCall pins the property the whole cheap path rests on: this resolution
// is reached on every observation cycle for a service the connector cannot judge, so it must never fetch an
// image config. resolveECSRuntime is given a nil ImageInspector for exactly that reason, and a fake that fails
// the test if called is the only way to keep that from being quietly undone.
func TestTaskDefinitionRuntimeMakesNoRegistryCall(t *testing.T) {
	td := taskDefOf(container("app", "registry.example.com/checkout:1.4", true, "JAVA_VERSION", "21.0.2"))
	// A container the resolution would otherwise want an image config for: no marker, and an image reference
	// that names no runtime at all.
	td.ContainerDefinitions = append(td.ContainerDefinitions,
		container("worker", "registry.example.com/opaque:1.4", false))

	// Held the way the observer holds one, so a change that started threading i.images into this resolution
	// would fail here rather than quietly adding a registry fetch per container per cycle.
	inspector := &countingImageInspector{}
	_ = &ecsInspector{images: inspector}
	if lang, _ := taskDefinitionRuntime(context.Background(), td); lang != LangJava {
		t.Fatalf("language = %q, want %q", lang, LangJava)
	}
	if inspector.calls != 0 {
		t.Fatalf("taskDefinitionRuntime read %d image configs; it must read none", inspector.calls)
	}
}

type countingImageInspector struct{ calls int }

func (c *countingImageInspector) ImageConfig(context.Context, string) (*ImageConfig, error) {
	c.calls++
	return nil, nil
}

// TestTaskDefinitionRuntimeRanksSiblings mirrors selectTargets' pass 2. The gate is RELATIVE, so a proxy that
// merely MENTIONS a runtime must not decide the workload's language when a sibling carries a real signal — and
// must not turn a single-language task into "multiple" either, which is what would erase the language from the
// central UI for the commonest multi-container shape there is.
func TestTaskDefinitionRuntimeRanksSiblings(t *testing.T) {
	cases := []struct {
		name    string
		td      *ecstypes.TaskDefinition
		wantLan string
		wantVer string
	}{
		{
			// nginx looks like PHP only because its image reference says so; php-fpm declares PHP_VERSION.
			// The strong signal wins outright and the version comes with it.
			name: "a strong signal outranks a weak sibling",
			td: taskDefOf(
				container("nginx", "nginx:1.27-php-fcgi", true),
				container("app", "registry.example.com/checkout:1.4", true, "PHP_VERSION", "8.3.13"),
			),
			wantLan: LangPHP,
			wantVer: "8.3",
		},
		{
			// Both weak, both correct — the ranking is a comparison, not a threshold, so this still resolves.
			name: "two weak signals that agree still resolve",
			td: taskDefOf(
				container("api", "openjdk:21.0-jre", true),
				container("batch", "openjdk:21.0-jre", true),
			),
			wantLan: LangJava,
			wantVer: "21.0",
		},
		{
			// Genuinely two applications in one task definition. No single value is true, and the container
			// layer is where the real answers live.
			name: "two languages at equal confidence report multiple",
			td: taskDefOf(
				container("api", "registry.example.com/api:1", true, "JAVA_VERSION", "21.0.2"),
				container("worker", "registry.example.com/worker:1", true, "PYTHON_VERSION", "3.12.1"),
			),
			wantLan: runtimeLanguageMultiple,
			wantVer: "",
		},
		{
			// One language, two versions: the language is still the workload's, the version is not.
			name: "same language at different versions drops the version",
			td: taskDefOf(
				container("api", "registry.example.com/api:1", true, "PHP_VERSION", "8.3.13"),
				container("cli", "registry.example.com/cli:1", true, "PHP_VERSION", "8.2.20"),
			),
			wantLan: LangPHP,
			wantVer: "",
		},
		{
			// Non-essential containers are not the application, so a sidecar's runtime never becomes the
			// workload's — the same candidate set the mutating path uses.
			name: "a non-essential sidecar is not the workload's language",
			td: taskDefOf(
				container("app", "registry.example.com/api:1", true, "JAVA_VERSION", "21.0.2"),
				container("logrouter", "registry.example.com/fluentbit:1", false, "PYTHON_VERSION", "3.12.1"),
			),
			wantLan: LangJava,
			wantVer: "21.0",
		},
		{
			// Odigos' own agent-staging sidecar is never a candidate, in either mode.
			name: "the odigos agents sidecar is never the workload's language",
			td: taskDefOf(
				container("app", "registry.example.com/api:1", true, "JAVA_VERSION", "21.0.2"),
				container(sidecarContainerName, testAgentsImage, false, "PYTHON_VERSION", "3.12.1"),
			),
			wantLan: LangJava,
			wantVer: "21.0",
		},
		{
			// Nothing to go on. Reporting "" is the honest answer, and the report already names the remedy
			// (a runtime override) separately.
			name:    "no signal at all resolves to nothing",
			td:      taskDefOf(container("app", "registry.example.com/opaque@sha256:abc", true)),
			wantLan: "",
			wantVer: "",
		},
		{
			// No essential container to instrument: candidateContainerIndexes errors, and that is not a
			// language answer.
			name:    "no instrumentable container resolves to nothing",
			td:      taskDefOf(container("job", "openjdk:21-jre", false)),
			wantLan: "",
			wantVer: "",
		},
		{
			name:    "a nil task definition resolves to nothing",
			td:      nil,
			wantLan: "",
			wantVer: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lang, ver := taskDefinitionRuntime(context.Background(), tc.td)
			if lang != tc.wantLan || ver != tc.wantVer {
				t.Fatalf("runtime = (%q, %q), want (%q, %q)", lang, ver, tc.wantLan, tc.wantVer)
			}
		})
	}
}

// TestTaskDefRuntimeToStoreOnlyFillsGaps is the guard that keeps the cheap path from damaging the rich one.
// refreshOwnership runs in strictly poorer conditions than a full observation — no image config, no source
// overrides — so it may fill in what nothing has recorded, and must not talk over what a better-informed pass
// established.
func TestTaskDefRuntimeToStoreOnlyFillsGaps(t *testing.T) {
	cases := []struct {
		name                  string
		lang, ver             string
		storedLang, storedVer string
		wantLang, wantVer     string
		wantWrite             bool
	}{
		{
			// The bug: an adopted, already-instrumented service with nothing recorded.
			name: "fills an empty record", lang: LangJava, ver: "21.0",
			wantLang: LangJava, wantVer: "21.0", wantWrite: true,
		},
		{
			// Nothing resolved. A pass that cannot see the language must not erase one.
			name:       "an empty resolution never erases a stored language",
			storedLang: LangPHP, storedVer: "8.3", wantWrite: false,
		},
		{
			// The regression this rule exists for: a customer deletes their destination, every pass drops to
			// refreshOwnership, and php's version — which its agent path is templated on — must survive.
			name: "a version this read cannot see is carried forward, not blanked",
			lang: LangPHP, storedLang: LangPHP, storedVer: "8.3", wantWrite: false,
		},
		{
			// Same language, and now a version too. Worth the write.
			name: "fills in a version the record was missing",
			lang: LangPHP, ver: "8.3", storedLang: LangPHP,
			wantLang: LangPHP, wantVer: "8.3", wantWrite: true,
		},
		{
			// Steady state: no write, every cycle.
			name: "an unchanged resolution writes nothing",
			lang: LangJava, ver: "21.0", storedLang: LangJava, storedVer: "21.0", wantWrite: false,
		},
		{
			// The live task definition is what the service actually runs now; a stored language describing a
			// revision it has moved off is stale, and this read is the only one available.
			name: "a redeploy to a different language is recorded",
			lang: LangNodeJS, ver: "20.11", storedLang: LangJava, storedVer: "21.0",
			wantLang: LangNodeJS, wantVer: "20.11", wantWrite: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lang, ver, ok := taskDefRuntimeToStore(tc.lang, tc.ver, tc.storedLang, tc.storedVer)
			if ok != tc.wantWrite {
				t.Fatalf("write = %v, want %v", ok, tc.wantWrite)
			}
			if !ok {
				return
			}
			if lang != tc.wantLang || ver != tc.wantVer {
				t.Fatalf("runtime to store = (%q, %q), want (%q, %q)", lang, ver, tc.wantLang, tc.wantVer)
			}
		})
	}
}

// TestAdoptedInstrumentedTaskReportsItsLanguage is the reported bug, stated at the level the two halves meet.
// A Fargate service adopted from LIVE instrumentation, on a connector with no OTLP destination yet, is observed
// by refreshOwnership alone — the path that used to keep only the instrumented boolean. Composing the two pure
// halves that path now calls proves the language reaches the service row, and that the plan follows it.
func TestAdoptedInstrumentedTaskReportsItsLanguage(t *testing.T) {
	// A task definition exactly as an Odigos IaC artifact leaves it: the app container carries the IaC marker
	// naming the staged agent, and the agents sidecar stages it. The image is digest-pinned, so the marker is
	// the ONLY thing here that knows the language — which is the situation from the bug report.
	td := taskDefOf(
		container("app", "registry.example.com/checkout@sha256:abc", true,
			store.EnvOdigosIaC, `{"v":1,"lang":"java","image":"`+testAgentsImage+`"}`),
		container(sidecarContainerName, testAgentsImage, false),
	)
	if len(instrumentedContainerIndexes(td)) == 0 {
		t.Fatal("test setup: the task definition must read as instrumented, or this case is vacuous")
	}

	// The service row as the bug report found it in ecs_services: instrumented, ownership revision stamped, and
	// no runtime language at all.
	svc := store.ECSService{Instrumented: true, OwnershipCheckedRevision: "arn:...:task-definition/catalog:7"}

	lang, ver := taskDefinitionRuntime(context.Background(), td)
	lang, ver, ok := taskDefRuntimeToStore(lang, ver, svc.RuntimeLanguage, svc.RuntimeVersion)
	if !ok {
		t.Fatal("an instrumented service with no recorded runtime must get one written")
	}
	if lang != LangJava {
		t.Fatalf("language = %q, want %q — the marker records it", lang, LangJava)
	}

	// The same value is the report's flat field, which is what the central UI renders as the language and what
	// the planner needs before it will publish IaC instructions at all.
	if got, _ := fargatePlanRuntime(baseapi.DiscoveredWorkload{RuntimeLanguage: lang, RuntimeVersion: ver}); got != LangJava {
		t.Fatalf("plan runtime = %q, want %q: a recorded language must also produce IaC instructions", got, LangJava)
	}
}

// TestObservedRuntimePrefersResolvedTargetsButNeverErases covers the observer's side of the decision: which of
// the two resolutions a pass records, and — the point of the whole guard — that a pass which resolved NOTHING
// never blanks a language out of the central UI.
func TestObservedRuntimePrefersResolvedTargetsButNeverErases(t *testing.T) {
	// A task definition whose language is plain from the task definition alone, so the fallback has something to
	// find whenever target selection does not.
	javaTaskDef := taskDefOf(container("app", "registry.example.com/api:1", true, "JAVA_VERSION", "21.0.2"))

	cases := []struct {
		name              string
		td                *ecstypes.TaskDefinition
		targets           []containerTarget
		svc               store.ECSService
		wantLang, wantVer string
		wantWrite         bool
	}{
		{
			// The fully-resolved pass is authoritative: it had the image config and the source's overrides.
			name:      "resolved targets win over the task definition read",
			td:        javaTaskDef,
			targets:   oneTarget(0, testRecipe(t, LangPython, ""), testExport()),
			wantLang:  LangPython,
			wantWrite: true,
		},
		{
			// ...including over a stored value, which is how a redeploy or a changed override lands.
			name:      "resolved targets replace a stored language",
			td:        javaTaskDef,
			targets:   oneTarget(0, testRecipe(t, LangPython, ""), testExport()),
			svc:       store.ECSService{RuntimeLanguage: LangJava, RuntimeVersion: "21.0"},
			wantLang:  LangPython,
			wantWrite: true,
		},
		{
			// A resolved aggregate that already matches the record still comes back with write set: whether the
			// value MOVED is recordRuntime's single question, and asking it twice is how the two answers drift.
			name:      "resolved targets that match the record are still returned",
			td:        javaTaskDef,
			targets:   oneTarget(0, testRecipe(t, LangPython, ""), testExport()),
			svc:       store.ECSService{RuntimeLanguage: LangPython},
			wantLang:  LangPython,
			wantWrite: true,
		},
		{
			// refreshOwnership: nil targets because it never looked. The task definition still says java, and
			// this is the bug — it used to record nothing at all.
			name:      "nil targets fall back to the task definition",
			td:        javaTaskDef,
			targets:   nil,
			wantLang:  LangJava,
			wantVer:   "21.0",
			wantWrite: true,
		},
		{
			// Selection resolved nothing — an uncoverable language, an undeterminable php version, a transport
			// the destination cannot carry. The language is still known and must not be erased.
			name:      "an empty target set never erases a stored language",
			td:        taskDefOf(container("app", "registry.example.com/opaque@sha256:abc", true)),
			targets:   nil,
			svc:       store.ECSService{RuntimeLanguage: LangPHP, RuntimeVersion: "8.3"},
			wantWrite: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lang, ver, ok := observedRuntime(context.Background(), tc.td, tc.targets, tc.svc)
			if ok != tc.wantWrite {
				t.Fatalf("write = %v, want %v (runtime %q/%q)", ok, tc.wantWrite, lang, ver)
			}
			if !ok {
				return
			}
			if lang != tc.wantLang || ver != tc.wantVer {
				t.Fatalf("runtime = (%q, %q), want (%q, %q)", lang, ver, tc.wantLang, tc.wantVer)
			}
		})
	}
}
