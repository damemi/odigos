package odigosinterrogationprofilesexporter

import (
	"testing"

	"github.com/odigos-io/odigos/common/interrogation"
)

func TestIsRuntimeProbeFrame(t *testing.T) {
	runtime := []string{
		"java.lang.Object java.util.HashMap.get(java.lang.Object)",
		"vtable chunks",
		"StubRoutines (final stubs) [arrayof_jbyte_arraycopy]",
	}
	for _, name := range runtime {
		if !IsRuntimeProbeFrame(name) {
			t.Fatalf("expected runtime frame %q", name)
		}
	}
	app := []string{
		"org.springframework.http.ResponseEntity com.rowan.notify.DocumentController.create(com.rowan.notify.DocumentRequest)",
		"void com.rowan.notify.RenderMessageProducer.publish(java.lang.String, com.rowan.notify.DocumentRequest)",
	}
	for _, name := range app {
		if IsRuntimeProbeFrame(name) {
			t.Fatalf("expected app frame %q", name)
		}
	}
}

func TestRootFirstJavaFramesFiltersRuntime(t *testing.T) {
	names, types := rootFirstJavaFrames(
		[]string{
			"void com.rowan.notify.Service.run()",
			"java.lang.Object java.util.HashMap.get(java.lang.Object)",
			"com.rowan.notify.Controller.entry",
		},
		[]string{"jvm", "jvm", "jvm"},
	)
	if len(names) != 2 {
		t.Fatalf("got %v", names)
	}
	if names[0] != "com.rowan.notify.Controller.entry" || names[1] != "void com.rowan.notify.Service.run()" {
		t.Fatalf("got %v", names)
	}
	if len(types) != 2 {
		t.Fatalf("types %v", types)
	}
}

func interrogationIsLibrary(name string) bool {
	return interrogation.ClassifyFrame(name) == interrogation.FrameKindLibrary
}
