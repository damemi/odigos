package instrumentationrules

import (
	"errors"
	"testing"
)

func TestCustomProbe(t *testing.T) {
	meta := CustomProbeMetadata{Notes: "why", Category: CustomProbeCategoryPerformance}
	tests := []struct {
		name   string
		probe  CustomProbe
		target string
		count  func(*CustomInstrumentationsWithMetadata) int
	}{
		{
			name:   "java",
			probe:  CustomProbe{Language: CustomProbeLanguageJava, ClassName: "com.acme.Cart", MethodName: "total"},
			target: "com.acme.Cart.total",
			count:  func(ci *CustomInstrumentationsWithMetadata) int { return len(ci.Java) },
		},
		{
			name:   "go function",
			probe:  CustomProbe{Language: CustomProbeLanguageGo, PackageName: "net/http", FunctionName: "ListenAndServe"},
			target: "net/http.ListenAndServe",
			count:  func(ci *CustomInstrumentationsWithMetadata) int { return len(ci.Golang) },
		},
		{
			name:   "go receiver method",
			probe:  CustomProbe{Language: CustomProbeLanguageGo, PackageName: "net/http", ReceiverName: "response", ReceiverMethodName: "WriteHeader"},
			target: "net/http.(*response).WriteHeader",
			count:  func(ci *CustomInstrumentationsWithMetadata) int { return len(ci.Golang) },
		},
		{
			name:   "cpp",
			probe:  CustomProbe{Language: CustomProbeLanguageCpp, Signature: "SSL_write"},
			target: "SSL_write",
			count:  func(ci *CustomInstrumentationsWithMetadata) int { return len(ci.Cpp) },
		},
		{
			name:   "php method",
			probe:  CustomProbe{Language: CustomProbeLanguagePhp, ClassName: "Cart", FunctionName: "total"},
			target: "Cart::total",
			count:  func(ci *CustomInstrumentationsWithMetadata) int { return len(ci.Php) },
		},
		{
			name:   "python",
			probe:  CustomProbe{Language: CustomProbeLanguagePython, Module: "app.billing", Qualname: "Invoice.calculate"},
			target: "app.billing:Invoice.calculate",
			count:  func(ci *CustomInstrumentationsWithMetadata) int { return len(ci.Python) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.probe.Target(); got != tt.target {
				t.Errorf("Target() = %q, want %q", got, tt.target)
			}
			ci, err := tt.probe.CustomInstrumentations(meta)
			if err != nil {
				t.Fatalf("CustomInstrumentations() error = %v", err)
			}
			if got := tt.count(ci); got != 1 {
				t.Errorf("probes of the language = %d, want 1", got)
			}
		})
	}
}

func TestCustomProbeInvalid(t *testing.T) {
	_, err := CustomProbe{Language: "cobol"}.CustomInstrumentations(CustomProbeMetadata{})
	if !errors.Is(err, ErrUnknownCustomProbeLanguage) {
		t.Errorf("unknown language error = %v, want ErrUnknownCustomProbeLanguage", err)
	}
	if got := (CustomProbe{Language: "cobol"}).Target(); got != "" {
		t.Errorf("unknown language Target() = %q, want empty", got)
	}
	// CustomInstrumentationsWithMetadata.Verify does not check cpp probes.
	if _, err := (CustomProbe{Language: CustomProbeLanguageCpp}).CustomInstrumentations(CustomProbeMetadata{}); err == nil {
		t.Error("cpp probe without a signature was accepted")
	}
	if _, err := (CustomProbe{Language: CustomProbeLanguageJava, ClassName: "A", MethodName: "b"}).CustomInstrumentations(CustomProbeMetadata{Category: "nope"}); err == nil {
		t.Error("invalid metadata was accepted")
	}
}
