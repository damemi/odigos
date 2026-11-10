package instrumentor

import (
	"testing"

	runpb "cloud.google.com/go/run/apiv2/runpb"
)

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"java": LangJava, "Kotlin": LangJava, "scala": LangJava,
		"python": LangPython, "PY": LangPython,
		"nodejs": LangNodeJS, "node": LangNodeJS, "javascript": LangNodeJS, "TypeScript": LangNodeJS,
		"dotnet": LangDotNet, ".NET": LangDotNet, "c#": LangDotNet, "csharp": LangDotNet,
		"":     "",
		"rust": "",
		"go":   "",
	}
	for in, want := range cases {
		if got := normalizeLanguage(in); got != want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessLanguageFromService(t *testing.T) {
	cases := []struct {
		img  string
		cmd  []string
		want string
	}{
		{img: "openjdk:21", want: LangJava},
		{img: "gcr.io/x/app", cmd: []string{"java", "-jar", "app.jar"}, want: LangJava},
		{img: "python:3.12", want: LangPython},
		{img: "gcr.io/x/app", cmd: []string{"gunicorn", "app:app"}, want: LangPython},
		{img: "node:20", want: LangNodeJS},
		{img: "mcr.microsoft.com/dotnet/aspnet:8.0", want: LangDotNet},
		{img: "gcr.io/x/opaque", want: ""},
	}
	for _, c := range cases {
		got := guessLanguageFromService(serviceWith(&runpb.Container{Image: c.img, Command: c.cmd}))
		if got != c.want {
			t.Errorf("guess(img=%q cmd=%v) = %q, want %q", c.img, c.cmd, got, c.want)
		}
	}
}

func TestGuessFromEnv(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{env: map[string]string{"JAVA_VERSION": "21"}, want: LangJava},
		{env: map[string]string{"JAVA_HOME": "/usr/lib/jvm/java-21"}, want: LangJava},
		{env: map[string]string{"PYTHON_VERSION": "3.12.1"}, want: LangPython},
		{env: map[string]string{"NODE_VERSION": "20.11.0"}, want: LangNodeJS},
		{env: map[string]string{"CORECLR_ENABLE_PROFILING": "1"}, want: LangDotNet},
		{env: map[string]string{"OTEL_DOTNET_AUTO_HOME": "/opt/otel"}, want: LangDotNet},
		{env: map[string]string{"JAVA_VERSION": ""}, want: ""}, // empty value is not a signal
		{env: map[string]string{"UNRELATED": "x"}, want: ""},
		{env: nil, want: ""},
	}
	for _, c := range cases {
		if got := guessFromEnv(c.env); got != c.want {
			t.Errorf("guessFromEnv(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestGuessFromCommandBasename(t *testing.T) {
	cases := []struct {
		cmd  []string
		want string
	}{
		{cmd: []string{"/usr/local/bin/python3.12", "-m", "app"}, want: LangPython},
		{cmd: []string{"python", "app.py"}, want: LangPython},
		{cmd: []string{"/opt/java/bin/java", "-jar", "app.jar"}, want: LangJava},
		{cmd: []string{"node", "server.js"}, want: LangNodeJS},
		{cmd: []string{"/usr/bin/npm", "start"}, want: LangNodeJS},
		{cmd: []string{"dotnet", "App.dll"}, want: LangDotNet},
		// Launchers are not the runtime executable -> no command-level match (image fallback handles these).
		{cmd: []string{"gunicorn", "app:app"}, want: ""},
		{cmd: nil, want: ""},
	}
	for _, c := range cases {
		if got := guessFromCommand(c.cmd); got != c.want {
			t.Errorf("guessFromCommand(%v) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

// TestGuessSignalPrecedence verifies env vars beat a conflicting image tag, and the command basename beats
// the image string, mirroring the strongest-first ordering ported from the OSS QuickScan/version-env logic.
func TestGuessSignalPrecedence(t *testing.T) {
	// A generic image, but a declared JAVA_HOME -> java (env beats image, which has no hint).
	svc := serviceWith(&runpb.Container{
		Image: "gcr.io/x/app",
		Env:   []*runpb.EnvVar{{Name: "JAVA_HOME", Values: &runpb.EnvVar_Value{Value: "/usr/lib/jvm"}}},
	})
	if got := guessLanguageFromService(svc); got != LangJava {
		t.Errorf("env signal should win, got %q", got)
	}

	// Node base image but the entry command is a python interpreter -> command basename beats image tag.
	svc = serviceWith(&runpb.Container{Image: "node:20", Command: []string{"python3", "app.py"}})
	if got := guessLanguageFromService(svc); got != LangPython {
		t.Errorf("command basename should beat image string, got %q", got)
	}
}
