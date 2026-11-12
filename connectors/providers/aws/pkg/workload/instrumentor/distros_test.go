package instrumentor

import "testing"

func TestLambdaDistroChoices(t *testing.T) {
	cases := []struct {
		lang, activation, name, want string
		invalid                      bool
	}{
		{"java", ActivationHandler, "", JavaAgentDistro, false},
		{"java", ActivationHandler, JavaAgentDistro, JavaAgentDistro, false},
		{"java", ActivationHandler, JavaWrapperDistro, JavaWrapperDistro, false},
		{"java", ActivationHandler, "java-lambda-otel-proxy-wrapper", "java-lambda-otel-proxy-wrapper", false},
		{"java", ActivationHandler, "java-lambda-otel-sqs-wrapper", "java-lambda-otel-sqs-wrapper", false},
		{"java", ActivationHandler, "java-lambda-otel-stream-wrapper", "java-lambda-otel-stream-wrapper", false},
		{"java", ActivationHandler, "otel-stream-handler", "java-lambda-otel-stream-wrapper", false},
		{"java", ActivationWebServer, JavaWrapperDistro, "", true},
		{"java", ActivationWebServer, "java-lambda-otel-stream-wrapper", "", true},
		{"java", ActivationHandler, "bogus", "", true},
		{"java", ActivationHandler, "java-ebpf-instrumented", "", true},
		{"java", ActivationHandler, "python-lambda-otel", "", true},
		{"nodejs", ActivationHandler, "nodejs-lambda-otel", "nodejs-lambda-otel", false},
		{"ruby", ActivationWebServer, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.lang+"/"+tc.activation+"/"+tc.name, func(t *testing.T) {
			d, err := ResolveLambdaDistro(tc.lang, tc.activation, tc.name)
			if tc.invalid {
				if err == nil {
					t.Fatal("unsupported selection accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Name != tc.want || d.Version == "" {
				t.Fatalf("got %+v", d)
			}
		})
	}
}

func TestSelectedJavaPlanUsesSelectedAdapter(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"", execWrapperAgent},
		{JavaAgentDistro, execWrapperAgent},
		{JavaWrapperDistro, execWrapperHandler},
		{"java-lambda-otel-proxy-wrapper", execWrapperProxyHandler},
		{"java-lambda-otel-sqs-wrapper", execWrapperSqsHandler},
		{"java-lambda-otel-stream-wrapper", execWrapperStreamHandler},
		{"otel-proxy-handler", execWrapperProxyHandler},
	} {
		p, err := DesiredLambdaPlanForDistro("java", ActivationHandler, tc.name)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range p.Fragments {
			for _, e := range f.Env {
				if e.Name == "AWS_LAMBDA_EXEC_WRAPPER" {
					found = true
					if e.Value != tc.want {
						t.Fatalf("%s: got %s want %s", tc.name, e.Value, tc.want)
					}
				}
			}
		}
		if !found {
			t.Fatal("plan omitted loader")
		}
		if !isOdigosExecWrapper(tc.want) {
			t.Fatalf("%s cannot be rolled back/migrated", tc.want)
		}
	}
}

func TestSupportedDistroCatalogsHaveOneDefaultPerLanguage(t *testing.T) {
	for _, lang := range []string{"java", "nodejs", "python", "ruby"} {
		defaults := 0
		for _, d := range LambdaDistros(lang, ActivationHandler) {
			if d.IsDefault {
				defaults++
			}
			if d.Language != lang || d.Version == "" {
				t.Fatalf("incorrect distro %+v", d)
			}
		}
		if defaults != 1 {
			t.Fatalf("%s: %d defaults", lang, defaults)
		}
	}
	for _, d := range FargateDistros() {
		if d.Version == "" || d.Version == "bundled" {
			t.Fatalf("missing bundled version: %+v", d)
		}
		if d.Language == "nodejs" && d.Name != "nodejs-fargate-otel" {
			t.Fatal("OpAMP-only Node distro exposed on Fargate")
		}
	}
}
