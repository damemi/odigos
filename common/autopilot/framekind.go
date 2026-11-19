// Package autopilot classifies JVM profile frame labels for autopilot call tries.
// The enterprise collector exporter, the autopilot loop and the UI API all classify
// with these rules, so ingest, AI evidence and the function map stay aligned.
package autopilot

import "strings"

// FrameKind is what a profile frame belongs to.
type FrameKind string

const (
	// FrameKindApp is customer code and the libraries it calls: probe candidates and AI evidence.
	FrameKindApp FrameKind = "app"
	// FrameKindRuntime is the JDK, the VM and third-party agent plumbing. Dropped at ingest.
	FrameKindRuntime FrameKind = "runtime"
	// FrameKindInstrumentation is the Odigos agent, running inside requests it instruments. Dropped at ingest.
	FrameKindInstrumentation FrameKind = "instrumentation"
)

var runtimePrefixes = []string{
	"java.", "javax.", "jakarta.", "sun.", "jdk.", "kotlin.", "scala.", "com.sun.", "org.jcp.",
	"net.bytebuddy.", "io.opentelemetry.",
}

// instrumentationPrefixes are the Odigos Java agent: its own classes, and ByteBuddy shaded into it. It
// transforms classes inside requests, so without these its frames would pass for the application's.
var instrumentationPrefixes = []string{
	"dev.keyval.ebpfJava.", "dev.keyval.net.bytebuddy.", "io.odigos.",
}

// agentAdviceMethods are the enter/exit hooks the Odigos Java agent weaves into instrumented methods.
// They run inside the instrumented class, so they can carry any package prefix.
var agentAdviceMethods = []string{".funcEnter", ".funcExit"}

// ClassifyFrame reports which FrameKind a JVM frame label belongs to.
func ClassifyFrame(name string) FrameKind {
	name = strings.TrimSpace(name)
	if name == "" {
		return FrameKindRuntime
	}
	lower := strings.ToLower(name)
	if strings.Contains(lower, "vtable") ||
		strings.HasPrefix(lower, "stubroutines") ||
		strings.Contains(lower, "_new_instance_java") {
		return FrameKindRuntime
	}
	class := ClassFromFrameName(name)
	if class == "" || hasAnyPrefix(class, runtimePrefixes) {
		return FrameKindRuntime
	}
	if hasAnyPrefix(class, instrumentationPrefixes) || hasAnySuffix(class, agentAdviceMethods) {
		return FrameKindInstrumentation
	}
	return FrameKindApp
}

// ClassFromFrameName extracts the qualified class/method prefix from a JVM frame label.
func ClassFromFrameName(name string) string {
	if i := strings.IndexByte(name, ' '); i > 0 {
		name = name[i+1:]
	}
	if j := strings.IndexByte(name, '('); j > 0 {
		name = name[:j]
	}
	if !strings.Contains(name, ".") {
		return ""
	}
	return name
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func hasAnySuffix(s string, suffixes []string) bool {
	for _, p := range suffixes {
		if strings.HasSuffix(s, p) {
			return true
		}
	}
	return false
}
