package source

import (
	"encoding/json"
	"github.com/odigos-io/agents-api/go/server/graph/model"
	"strings"
	"testing"
)

func TestDistroRoundTrip(t *testing.T) {
	name := "java-lambda-otel-stream-wrapper"
	in := model.CreateSourceInput{Name: "fn", Kind: "aws.lambda", Enabled: true, Config: &model.SourceConfigInput{DistroOverride: &model.DistroOverrideInput{OtelDistroName: &name}}}
	src := SourceFromCreateInput(in)
	got := src.ToSourceModel().Config.DistroOverride
	if got == nil || got.OtelDistroName == nil || *got.OtelDistroName != name {
		t.Fatalf("lost SDK choice: %+v", got)
	}
}

func TestLegacyDistroJSONMigration(t *testing.T) {
	for _, c := range []struct{ wrapper, want string }{
		{"otel-handler", "java-lambda-otel-wrapper"},
		{"otel-proxy-handler", "java-lambda-otel-proxy-wrapper"},
		{"otel-sqs-handler", "java-lambda-otel-sqs-wrapper"},
		{"otel-stream-handler", "java-lambda-otel-stream-wrapper"},
	} {
		var d DistroOverride
		if err := json.Unmarshal([]byte(`{"otel_distro_name":"java-lambda-otel-wrapper","handler_wrapper":"`+c.wrapper+`"}`), &d); err != nil {
			t.Fatal(err)
		}
		if d.OtelDistroName != c.want {
			t.Fatalf("%s: %+v", c.wrapper, d)
		}
		encoded, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "handler_wrapper") {
			t.Fatalf("legacy metadata persisted: %s", encoded)
		}
		var roundTrip DistroOverride
		if err := json.Unmarshal(encoded, &roundTrip); err != nil || roundTrip != d {
			t.Fatalf("round trip: %+v %v", roundTrip, err)
		}
	}
}
