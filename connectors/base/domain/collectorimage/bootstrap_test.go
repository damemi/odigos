package collectorimage

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

// bootstrapDocument and encodedBootstrapDocument pin the WIRE FORMAT across a module boundary. This
// connector encodes the value and `collector/cmd/odigosopampsupervisor` decodes it, the two are built
// and released separately, and its bootstrap_test.go asserts the same pair from its side.
//
// A skew here does not fail loudly: the agent refuses the document with a base64 error, logs it, and
// serves the inert configuration — reported as "the deployment supplied something unreadable" rather
// than as a version mismatch, which is a long way from the fix.
const bootstrapDocument = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
exporters:
  otlphttp/dest:
    endpoint: https://ingest.example.test
extensions:
  health_check:
    endpoint: 0.0.0.0:13133
service:
  extensions:
    - health_check
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [otlphttp/dest]
`

const encodedBootstrapDocument = "cmVjZWl2ZXJzOgogIG90bHA6CiAgICBwcm90b2NvbHM6CiAgICAgIGdycGM6CiAgICAgICAgZW5kcG9pbnQ6IDAuMC4wLjA6NDMxNwpleHBvcnRlcnM6CiAgb3RscGh0dHAvZGVzdDoKICAgIGVuZHBvaW50OiBodHRwczovL2luZ2VzdC5leGFtcGxlLnRlc3QKZXh0ZW5zaW9uczoKICBoZWFsdGhfY2hlY2s6CiAgICBlbmRwb2ludDogMC4wLjAuMDoxMzEzMwpzZXJ2aWNlOgogIGV4dGVuc2lvbnM6CiAgICAtIGhlYWx0aF9jaGVjawogIHBpcGVsaW5lczoKICAgIHRyYWNlczoKICAgICAgcmVjZWl2ZXJzOiBbb3RscF0KICAgICAgZXhwb3J0ZXJzOiBbb3RscGh0dHAvZGVzdF0K"

// TestTheEncodingIsTheContract, from this side of the boundary.
func TestTheEncodingIsTheContract(t *testing.T) {
	encoded := EncodeBootstrapConfig(bootstrapDocument)
	if encoded.Reveal() != encodedBootstrapDocument {
		t.Fatalf("the encoding changed:\n%s", encoded.Reveal())
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded.Reveal())
	if err != nil {
		t.Fatalf("what this connector produced is not base64: %v", err)
	}
	if string(decoded) != bootstrapDocument {
		t.Fatalf("round trip changed the document:\n%s", decoded)
	}

	// The ALPHABET, pinned separately, because the pinned document above happens to encode without
	// either of the two characters the standard and URL-safe alphabets disagree about — so it would pass
	// under both, and the mismatch would produce a document that decodes to nothing on roughly half of
	// all inputs and works on the rest.
	alphabet := EncodeBootstrapConfig("\xff\xff\xfe\xff\xff\xff").Reveal()
	if alphabet != "///+////" {
		t.Fatalf("the encoding alphabet is not standard base64: %q", alphabet)
	}
}

// TestTheEncodedDocumentIsASecretFromTheMomentItExists. The document carries the destination's own
// credential, so an encoder returning a plain string would put credential-bearing bytes back into
// ordinary circulation on their way to the secret store — where an added log line, a %v, or a
// marshalled struct is all it takes.
func TestTheEncodedDocumentIsASecretFromTheMomentItExists(t *testing.T) {
	encoded := EncodeBootstrapConfig(bootstrapDocument)

	for _, rendered := range []string{
		encoded.String(),
		encoded.GoString(),
		fmt.Sprint(encoded),
		fmt.Sprintf("%v", encoded),
		fmt.Sprintf("%s", encoded),
		fmt.Sprintf("%+v", encoded),
		fmt.Sprintf("%#v", encoded),
	} {
		if strings.Contains(rendered, "ingest.example.test") || strings.Contains(rendered, encodedBootstrapDocument) {
			t.Errorf("a rendering of the encoded document leaked it: %q", rendered)
		}
	}
	if _, err := encoded.MarshalJSON(); err == nil {
		t.Error("the encoded document can be marshalled to JSON, so a status structure could carry it out")
	}
}

// TestAnEmptyDocumentEncodesToNothing. "" and base64-of-nothing mean different things to a caller: one
// says there is nothing to install, and the other is a value to write into a secret — and a secret
// holding base64 of an empty string produces a Collector whose bootstrap document decodes to nothing.
func TestAnEmptyDocumentEncodesToNothing(t *testing.T) {
	got := EncodeBootstrapConfig("")
	if !got.Empty() {
		t.Fatalf("an empty document encoded to %q", got.Reveal())
	}
	// Specifically not base64 of nothing dressed up as a value: a caller installs whatever is not
	// Empty, so anything non-empty here becomes a secret version holding a document that decodes to no
	// configuration at all — which the agent then refuses, having replaced nothing.
	if got.Reveal() != "" {
		t.Errorf("an empty document encoded to %q, which a caller would install", got.Reveal())
	}
	if got := EncodeBootstrapConfig(bootstrapDocument); got.Empty() {
		t.Fatal("a real document encoded to nothing")
	}
}

// TestTheBootstrapConfigMayNeverBeAPlainEnvironmentValue. It is a CONFIGURATION rather than a token,
// which is exactly why it is easy to miss: a configuration carries its exporters' credentials, so an
// artifact that set it inline would put a customer's destination API key where every holder of
// ecs:DescribeTaskDefinition can read it.
func TestTheBootstrapConfigMayNeverBeAPlainEnvironmentValue(t *testing.T) {
	if !contains(SecretEnvVars(), EnvBootstrapConfig) {
		t.Fatalf("SecretEnvVars = %v, want %s among them", SecretEnvVars(), EnvBootstrapConfig)
	}
	problems := EnvProblems(map[string]string{EnvBootstrapConfig: encodedBootstrapDocument})
	if len(problems) != 1 || problems[0].Code != "SecretInPlainEnvironment" {
		t.Fatalf("problems = %v", problems)
	}
	if problems[0].Severity != collectorstatus.SeverityError {
		t.Errorf("severity = %q, want Error: nothing should be deployed with it inline", problems[0].Severity)
	}
	// And the value itself is not echoed into the message, which would move the leak from the task
	// definition into the status report.
	if strings.Contains(problems[0].Message, encodedBootstrapDocument) {
		t.Errorf("the problem message quotes the value: %q", problems[0].Message)
	}
}

// TestAMissingBootstrapRefIsNotAProblem. It is a mitigation, not a requirement: without one, a cold
// start during a control-plane outage serves the inert bundled configuration, which is v1's documented
// behaviour. Reporting its absence would put a standing fault on every Collector in a fleet that
// is behaving exactly as designed.
func TestAMissingBootstrapRefIsNotAProblem(t *testing.T) {
	problems := DeliveryProblems(Delivery{
		LicenseSecretRef:    "arn:aws:secretsmanager:eu-west-1:1:secret:license-AbCdEf",
		CredentialSecretRef: "arn:aws:secretsmanager:eu-west-1:1:secret:credential-AbCdEf",
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none over an absent optional ref", problems)
	}
}

// TestASharedBootstrapSecretIsRefused. Its lifetime is the CONFIGURATION's — rewritten whenever a
// destination changes — so a shared secret would overwrite a licence or a credential with a YAML
// document, and the Collector would then present a config file as its Authorization header.
func TestASharedBootstrapSecretIsRefused(t *testing.T) {
	const shared = "arn:aws:secretsmanager:eu-west-1:1:secret:everything-AbCdEf"
	for name, delivery := range map[string]Delivery{
		"shared with the licence": {
			LicenseSecretRef:    shared,
			CredentialSecretRef: "arn:aws:secretsmanager:eu-west-1:1:secret:credential-AbCdEf",
			BootstrapSecretRef:  shared,
		},
		"shared with the credential": {
			LicenseSecretRef:    "arn:aws:secretsmanager:eu-west-1:1:secret:license-AbCdEf",
			CredentialSecretRef: shared,
			BootstrapSecretRef:  shared,
		},
	} {
		t.Run(name, func(t *testing.T) {
			problems := DeliveryProblems(delivery)
			if !hasCode(problems, "BootstrapSharesASecret") {
				t.Fatalf("problems = %v, want BootstrapSharesASecret", problems)
			}
		})
	}

	// Three distinct secrets is the shape that works, and it reports nothing.
	problems := DeliveryProblems(Delivery{
		LicenseSecretRef:    "arn:aws:secretsmanager:eu-west-1:1:secret:license-AbCdEf",
		CredentialSecretRef: "arn:aws:secretsmanager:eu-west-1:1:secret:credential-AbCdEf",
		BootstrapSecretRef:  "arn:aws:secretsmanager:eu-west-1:1:secret:bootstrap-AbCdEf",
	})
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none over three distinct secrets", problems)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func hasCode(problems []collectorstatus.Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}
