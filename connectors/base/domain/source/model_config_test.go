package source

import (
	"strings"
	"testing"

	"github.com/odigos-io/agents-api/go/server/graph/model"
)

// The schema declares the per-kind branches but cannot express "exactly one, and it must agree with
// the source's kind" — GraphQL has no input unions and no cross-field constraints. Without this
// check a Lambda source would silently accept ECS container overrides that can never apply.
func TestValidateConfigInput(t *testing.T) {
	// A branch counts only when it carries content, so the fixtures below must be populated.
	withOverride := func() []*model.ContainerOverrideInput {
		return []*model.ContainerOverrideInput{{ContainerName: "app"}}
	}
	ecs := &model.SourceConfigInput{Ecs: &model.EcsSourceConfigInput{ContainerOverrides: withOverride()}}
	docker := &model.SourceConfigInput{Docker: &model.DockerSourceConfigInput{ContainerOverrides: withOverride()}}
	both := &model.SourceConfigInput{
		Ecs:    &model.EcsSourceConfigInput{ContainerOverrides: withOverride()},
		Docker: &model.DockerSourceConfigInput{ContainerOverrides: withOverride()},
	}
	plain := &model.SourceConfigInput{RuntimeOverride: &model.RuntimeOverrideInput{}}
	// What genqlient actually puts on the wire for a source with no container config at all.
	emptyBoth := &model.SourceConfigInput{Ecs: &model.EcsSourceConfigInput{}, Docker: &model.DockerSourceConfigInput{}}

	for _, tc := range []struct {
		name    string
		kind    string
		in      *model.SourceConfigInput
		wantErr string
	}{
		{"nil config", "aws.lambda", nil, ""},
		{"no branch", "aws.lambda", plain, ""},
		{"ecs branch on fargate", kindPrefixFargateTask, ecs, ""},
		{"ecs branch on ecs kind", kindPrefixECS, ecs, ""},
		{"ecs branch on lambda", "aws.lambda", ecs, "does not apply"},
		{"docker branch on fargate", kindPrefixFargateTask, docker, "does not apply"},
		{"two branches", kindPrefixFargateTask, both, "at most one"},
		// Regression: genqlient serializes every optional input object as `{}` instead of omitting
		// it, so odictl sends BOTH branches present-but-empty on every createSource. Treating
		// presence as intent rejected every source creation with "at most one kind-specific branch".
		// An empty branch is a client artifact and must say nothing.
		{"empty branches from a client that always serializes them", kindDocker, emptyBoth, ""},
		{"empty branches on a kind neither applies to", "aws.lambda", emptyBoth, ""},
	} {
		err := ValidateConfigInput(tc.kind, tc.in)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.wantErr)
		}
	}
}

// Container overrides must survive the wire round trip, including an explicit disable — a nil
// Enabled ("not specified") and a false one ("never instrument this") are different instructions and
// collapsing them would silently instrument a container the customer opted out of.
func TestContainerOverridesRoundTrip(t *testing.T) {
	disabled := true
	in := model.CreateSourceInput{
		Name: "svc", Kind: kindPrefixFargateTask, Enabled: true,
		Config: &model.SourceConfigInput{Ecs: &model.EcsSourceConfigInput{
			ContainerOverrides: []*model.ContainerOverrideInput{
				{ContainerName: "app", RuntimeOverride: &model.RuntimeOverrideInput{Language: strPtr("ruby"), Version: strPtr("3.3")}},
				{ContainerName: "proxy", Disabled: &disabled},
			},
		}},
	}

	src := SourceFromCreateInput(in)
	if len(src.Config.ContainerOverrides) != 2 {
		t.Fatalf("got %d overrides, want 2", len(src.Config.ContainerOverrides))
	}
	app := src.Config.ContainerOverrideFor("app")
	if app == nil || app.RuntimeOverride.Language != "ruby" || app.RuntimeOverride.Version != "3.3" {
		t.Errorf("app override = %+v, want ruby/3.3", app)
	}
	if !app.InstrumentationEnabled() {
		t.Error("an override with no explicit enabled must default to enabled")
	}
	proxy := src.Config.ContainerOverrideFor("proxy")
	if proxy == nil || proxy.InstrumentationEnabled() {
		t.Errorf("proxy override = %+v, want explicitly disabled", proxy)
	}
	if src.Config.ContainerOverrideFor("absent") != nil {
		t.Error("an unlisted container must have no override")
	}

	// And back out under the branch matching the kind — never under the other one.
	out := src.ToSourceModel()
	if out.Config == nil || out.Config.Ecs == nil || len(out.Config.Ecs.ContainerOverrides) != 2 {
		t.Fatalf("ecs branch not echoed: %+v", out.Config)
	}
	if out.Config.Docker != nil {
		t.Errorf("docker branch must stay unset for an ECS source: %+v", out.Config.Docker)
	}
	if got := out.Config.Ecs.ContainerOverrides[1]; got.Disabled == nil || !*got.Disabled {
		t.Errorf("explicit disable lost on the way out: %+v", got)
	}
	// An unremarkable container carries no flag at all rather than an explicit negative.
	if got := out.Config.Ecs.ContainerOverrides[0]; got.Disabled != nil {
		t.Errorf("a container with no opinion must send no disabled flag, got %v", *got.Disabled)
	}
}

// A kind with no container layer must not echo overrides at all, even if a stored row somehow holds
// them — there is no branch they belong under.
func TestContainerOverridesNotEchoedForKindWithoutContainers(t *testing.T) {
	s := &Source{Name: "fn", Kind: "aws.lambda", Config: SourceConfig{
		ContainerOverrides: []ContainerOverride{{ContainerName: "app"}},
	}}
	out := s.ToSourceModel()
	if out.Config != nil && (out.Config.Ecs != nil || out.Config.Docker != nil) {
		t.Errorf("lambda source echoed a container branch: %+v", out.Config)
	}
}

func strPtr(s string) *string { return &s }

// The other half of the same bug: reading the overrides must also skip an empty branch. Checking
// only "is the branch present" made a genqlient client's empty `ecs: {}` shadow the docker branch
// that actually carried the customer's overrides, silently dropping them.
func TestContainerOverridesIgnoreEmptyBranches(t *testing.T) {
	in := &model.SourceConfigInput{
		Ecs: &model.EcsSourceConfigInput{}, // always-serialized empty branch
		Docker: &model.DockerSourceConfigInput{ContainerOverrides: []*model.ContainerOverrideInput{
			{ContainerName: "app"},
		}},
	}
	if err := ValidateConfigInput(kindDocker, in); err != nil {
		t.Fatalf("an empty sibling branch must not fail validation: %v", err)
	}
	got := containerOverridesFromInput(in)
	if len(got) != 1 || got[0].ContainerName != "app" {
		t.Errorf("overrides = %+v, want the docker branch's, not the empty ecs one", got)
	}

	// Both empty: nothing configured.
	empty := &model.SourceConfigInput{Ecs: &model.EcsSourceConfigInput{}, Docker: &model.DockerSourceConfigInput{}}
	if got := containerOverridesFromInput(empty); got != nil {
		t.Errorf("empty branches = %+v, want no overrides", got)
	}
}
