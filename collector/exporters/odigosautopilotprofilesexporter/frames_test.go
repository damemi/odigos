package odigosautopilotprofilesexporter

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootFirstJavaFrames(t *testing.T) {
	names, types := rootFirstJavaFrames(
		[]string{"com.app.Leaf.run", "com.app.Mid.run", "com.app.Root.run", "native"},
		[]string{"hotspot", "jvm", "hotspot", "native"},
	)
	assert.Equal(t, []string{"com.app.Root.run", "com.app.Mid.run", "com.app.Leaf.run"}, names)
	assert.Equal(t, []string{"hotspot", "jvm", "hotspot"}, types)
}

func TestRootFirstJavaFramesDropsInstrumentation(t *testing.T) {
	names, types := rootFirstJavaFrames(
		[]string{
			"void com.app.Service.handle()",
			"void dev.keyval.ebpfJava.utils.ThreadContext.restoreContext(java.lang.Object[])",
			"java.lang.String io.odigos.spring.servlet.SpringServletAdvice.funcEnter(java.lang.String, java.lang.String, long, byte[], long, java.lang.String, java.lang.String)",
			"void io.odigos.spring.servlet.SpringServletAdvice.funcExit(int, byte[], byte[])",
			"com.app.Controller.entry",
		},
		[]string{"jvm", "jvm", "jvm", "jvm", "jvm"},
	)
	assert.Equal(t, []string{"com.app.Controller.entry", "void com.app.Service.handle()"}, names)
	assert.Equal(t, []string{"jvm", "jvm"}, types)
}

const (
	testSpring = "void org.springframework.web.servlet.DispatcherServlet.doDispatch()"
	testTomcat = "void org.apache.catalina.core.StandardWrapperValve.invoke()"
	testJDK    = "java.lang.Object java.util.HashMap.get(java.lang.Object)"
)

func jvmTypes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "jvm"
	}
	return out
}

func TestRootFirstJavaFramesKeepsLibraryFrames(t *testing.T) {
	in := []string{"com.app.Leaf.run", testSpring, "void org.springframework.web.servlet.FrameworkServlet.service()", testJDK, testTomcat, "com.app.Root.run"}
	names, types := rootFirstJavaFrames(in, jvmTypes(len(in)))
	assert.Equal(t, []string{"com.app.Root.run", testTomcat, "void org.springframework.web.servlet.FrameworkServlet.service()", testSpring, "com.app.Leaf.run"}, names)
	assert.Equal(t, jvmTypes(5), types)
}

func TestRootFirstJavaFramesCapKeepsLeafMost(t *testing.T) {
	var in []string
	for i := 0; i < 80; i++ {
		in = append(in, fmt.Sprintf("com.app.F%d.run", i))
	}
	names, types := rootFirstJavaFrames(in, jvmTypes(len(in)))
	assert.Len(t, names, 64)
	assert.Len(t, types, 64)
	// leaf-most 64 frames (F0..F63), root-first.
	assert.Equal(t, "com.app.F63.run", names[0])
	assert.Equal(t, "com.app.F0.run", names[63])
}
