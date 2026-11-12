package instrumentor

import (
	"errors"
	"strings"
	"testing"
)

// The recipes are sourced from the OSS Odigos distros module — these tests pin the derived activation
// env against the agents-image layout, so a distros-module version bump that moves agent paths fails
// loudly here instead of silently injecting dead env.

func TestRecipeJava(t *testing.T) {
	rec := testRecipe(t, LangJava, "")
	if len(rec.appendEnv) != 1 || rec.appendEnv[0].name != "JAVA_TOOL_OPTIONS" {
		t.Fatalf("java appendEnv = %+v", rec.appendEnv)
	}
	if got := rec.appendEnv[0].evaluate(""); got != "-javaagent:/var/odigos/java/javaagent.jar" {
		t.Errorf("java empty-original append = %q (leading delimiter must be stripped)", got)
	}
	if got := rec.appendEnv[0].evaluate("-Xmx1g"); got != "-Xmx1g -javaagent:/var/odigos/java/javaagent.jar" {
		t.Errorf("java append = %q, want customer value first", got)
	}
	if !contains(rec.agentDirs, "java") {
		t.Errorf("java recipe dirs=%v", rec.agentDirs)
	}
	// java-community declares otlpHttpLocalNode, so its preferred transport is http/protobuf — not the
	// gRPC we used to assume. It is not pinned, though: the Java agent honors OTEL_EXPORTER_OTLP_PROTOCOL,
	// so a gRPC-only destination still works via the fallback.
	if rec.otlpProtocol != otlpProtocolHTTP {
		t.Errorf("java protocol = %q, want http/protobuf (the distro declares otlpHttpLocalNode)", rec.otlpProtocol)
	}
	if rec.protocolFixed {
		t.Error("java must not be protocol-pinned — its SDK honors OTEL_EXPORTER_OTLP_PROTOCOL")
	}
}

// Every OSS community distro declares otlpHttpLocalNode, so every recipe must prefer http/protobuf. This
// is the regression guard for the bug where only php/ruby (which state the protocol explicitly) got it
// and the rest silently defaulted to gRPC.
func TestAllRecipesPreferDistroDeclaredProtocol(t *testing.T) {
	for _, lc := range []struct{ lang, version string }{
		{LangJava, ""}, {LangPython, ""}, {LangNodeJS, ""}, {LangDotNet, ""}, {LangPHP, "8.3"}, {LangRuby, "3.3"},
	} {
		rec := testRecipe(t, lc.lang, lc.version)
		if rec.otlpProtocol != otlpProtocolHTTP {
			t.Errorf("%s protocol = %q, want http/protobuf", lc.lang, rec.otlpProtocol)
		}
		// Only php/ruby pin it (their community SDKs are http/protobuf-only).
		wantFixed := lc.lang == LangPHP || lc.lang == LangRuby
		if rec.protocolFixed != wantFixed {
			t.Errorf("%s protocolFixed = %v, want %v", lc.lang, rec.protocolFixed, wantFixed)
		}
	}
}

func TestRecipePython(t *testing.T) {
	rec := testRecipe(t, LangPython, "")
	if rec.staticEnv["OTEL_PYTHON_CONFIGURATOR"] != "odigos-python-configurator" {
		t.Errorf("python staticEnv = %v", rec.staticEnv)
	}
	if len(rec.appendEnv) != 1 || rec.appendEnv[0].name != "PYTHONPATH" {
		t.Fatalf("python appendEnv = %+v", rec.appendEnv)
	}
	want := "/var/odigos/python:/var/odigos/python/opentelemetry/instrumentation/auto_instrumentation"
	if got := rec.appendEnv[0].evaluate(""); got != want {
		t.Errorf("python empty-original append = %q, want %q", got, want)
	}
	if got := rec.appendEnv[0].evaluate("/app"); got != "/app:"+want {
		t.Errorf("python append = %q", got)
	}
}

func TestRecipeDotNet(t *testing.T) {
	rec := testRecipe(t, LangDotNet, "")
	if got := rec.staticEnv["CORECLR_PROFILER_PATH"]; !strings.Contains(got, "linux-glibc") {
		t.Errorf("dotnet profiler path = %q, want the glibc (LIBC_TYPE) variant", got)
	}
	if rec.staticEnv["CORECLR_ENABLE_PROFILING"] != "1" {
		t.Errorf("dotnet staticEnv = %v", rec.staticEnv)
	}
}

func TestRecipeNodeJSDivergesFromOpAMPDistro(t *testing.T) {
	rec := testRecipe(t, LangNodeJS, "")
	if len(rec.appendEnv) != 1 || rec.appendEnv[0].name != EnvNodeOptions {
		t.Fatalf("nodejs appendEnv = %+v", rec.appendEnv)
	}
	got := rec.appendEnv[0].evaluate("")
	if !strings.Contains(got, "@opentelemetry/auto-instrumentations-node/build/src/register.js") {
		t.Errorf("nodejs must use the upstream env-driven agent (nodejs-community is OpAMP-driven), got %q", got)
	}
	// The upstream agent must not depend on an OpAMP server env.
	for k := range rec.staticEnv {
		if strings.Contains(k, "OPAMP") {
			t.Errorf("nodejs recipe must not require OpAMP env, found %q", k)
		}
	}
}

func TestRecipePHPRubyVersionTemplating(t *testing.T) {
	php := testRecipe(t, LangPHP, "8.3")
	if got := php.staticEnv["PHP_INI_SCAN_DIR"]; got != ":/var/odigos/php/8.3" {
		t.Errorf("php PHP_INI_SCAN_DIR = %q", got)
	}
	if php.otlpProtocol != otlpProtocolHTTP {
		t.Errorf("php protocol = %q, want http/protobuf (the PHP SDK is http-only)", php.otlpProtocol)
	}
	if _, ok := php.staticEnv[EnvOtlpProtocol]; ok {
		t.Error("the export protocol must be lifted out of staticEnv (the injector owns OTLP export vars)")
	}

	ruby := testRecipe(t, LangRuby, "3.3")
	if got := ruby.staticEnv["RUBYOPT"]; got != "-r/var/odigos/ruby/3.3/index.rb" {
		t.Errorf("ruby RUBYOPT = %q", got)
	}
	if ruby.otlpProtocol != otlpProtocolHTTP {
		t.Errorf("ruby protocol = %q, want http/protobuf", ruby.otlpProtocol)
	}

	// Version-templated languages without a resolved version are a targeted error (the caller renders it
	// as unsupported-with-hint).
	var r recipes
	if _, err := r.recipeFor(LangPHP, ""); !errors.Is(err, errNeedsRuntimeVersion) {
		t.Errorf("php without version: err = %v, want errNeedsRuntimeVersion", err)
	}
	if _, err := r.recipeFor(LangRuby, ""); !errors.Is(err, errNeedsRuntimeVersion) {
		t.Errorf("ruby without version: err = %v, want errNeedsRuntimeVersion", err)
	}
}

func TestCoverableAndNormalize(t *testing.T) {
	for _, lang := range []string{LangJava, LangPython, LangNodeJS, LangDotNet, LangPHP, LangRuby} {
		if !CoverableLanguage(lang) {
			t.Errorf("%s should be coverable", lang)
		}
	}
	for _, lang := range []string{"go", "golang", "", "cobol"} {
		if CoverableLanguage(lang) {
			t.Errorf("%q should not be coverable", lang)
		}
	}
	aliases := map[string]string{
		"Java": LangJava, "kotlin": LangJava, "scala": LangJava,
		"py": LangPython, "node.js": LangNodeJS, "TypeScript": LangNodeJS,
		"C#": LangDotNet, ".NET": LangDotNet,
		"PHP": LangPHP, "rb": LangRuby, "Ruby": LangRuby,
		"go": "", "unknown": "",
	}
	for in, want := range aliases {
		if got := normalizeLanguage(in); got != want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
	// A DETECTED-but-uncoverable language and an UNDETECTED one are different situations and must read
	// differently: the first is a hard platform limit no override can fix, the second is actionable.
	goReason := uncoverableLanguageReason("go")
	if !strings.Contains(goReason, "eBPF") || !strings.Contains(goReason, "cannot enable this") {
		t.Errorf("the Go reason should explain the eBPF limit and that an override will not help, got %q", goReason)
	}
	if !strings.Contains(goReason, "detected Go") {
		t.Errorf("the Go reason should say the language was detected, got %q", goReason)
	}
	undetected := uncoverableLanguageReason("")
	if !strings.Contains(undetected, "could not determine") || !strings.Contains(undetected, "runtime override") {
		t.Errorf("the undetermined-language reason should say so and point at the source override, got %q", undetected)
	}
	if strings.Contains(undetected, "detected") && !strings.Contains(undetected, "could not") {
		t.Errorf("the undetermined reason must not claim a detection, got %q", undetected)
	}
}

func TestDotNetRecipeMatchesSelfContainedAgent(t *testing.T) {
	rec := testRecipe(t, LangDotNet, "")
	for _, name := range []string{"DOTNET_ADDITIONAL_DEPS", "DOTNET_SHARED_STORE"} {
		if value, ok := rec.staticEnv[name]; ok {
			t.Errorf("obsolete %s=%s", name, value)
		}
	}
	if rec.staticEnv["DOTNET_STARTUP_HOOKS"] != "/var/odigos/dotnet/net/OpenTelemetry.AutoInstrumentation.StartupHook.dll" {
		t.Fatal("missing startup hook")
	}
	if rec.staticEnv["CORECLR_PROFILER_PATH"] != "/var/odigos/dotnet/linux-glibc/OpenTelemetry.AutoInstrumentation.Native.so" {
		t.Fatal("missing native profiler")
	}
}
