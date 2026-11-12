package odigosinterrogationprofilesexporter

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

func TestRootFirstJavaFramesCollapsesLibraryRun(t *testing.T) {
	// leaf-first: app leaf, spring, spring, tomcat, app root
	in := []string{"com.app.Leaf.run", testSpring, "void org.springframework.web.servlet.FrameworkServlet.service()", testTomcat, "com.app.Root.run"}
	names, types := rootFirstJavaFrames(in, jvmTypes(len(in)))
	assert.Equal(t, []string{"com.app.Root.run", testTomcat, "com.app.Leaf.run"}, names)
	assert.Equal(t, jvmTypes(3), types)
}

func TestRootFirstJavaFramesRuntimeDoesNotSplitLibraryRun(t *testing.T) {
	in := []string{"com.app.Leaf.run", testSpring, testJDK, testTomcat, "com.app.Root.run"}
	names, _ := rootFirstJavaFrames(in, jvmTypes(len(in)))
	assert.Equal(t, []string{"com.app.Root.run", testTomcat, "com.app.Leaf.run"}, names)
}

func TestRootFirstJavaFramesSeparateLibraryRunsStaySeparate(t *testing.T) {
	in := []string{"com.app.Leaf.run", testSpring, "com.app.Mid.run", testTomcat, "com.app.Root.run"}
	names, _ := rootFirstJavaFrames(in, jvmTypes(len(in)))
	assert.Equal(t, []string{"com.app.Root.run", testTomcat, "com.app.Mid.run", testSpring, "com.app.Leaf.run"}, names)
}

func TestRootFirstJavaFramesCapDropsLibraryBeforeApp(t *testing.T) {
	// 62 app frames with a library frame between each of the 4 root-most pairs.
	var in []string
	for i := 0; i < 62; i++ {
		in = append(in, fmt.Sprintf("com.app.F%d.run", i))
		if i >= 58 {
			in = append(in, fmt.Sprintf("org.springframework.lib%d.Run.go", i))
		}
	}
	// 62 app + 4 library = 66 > 64: the two root-most library frames go.
	names, types := rootFirstJavaFrames(in, jvmTypes(len(in)))
	assert.Len(t, names, 64)
	assert.Len(t, types, 64)
	libs := 0
	for _, n := range names {
		if interrogationIsLibrary(n) {
			libs++
		}
	}
	assert.Equal(t, 2, libs)
	assert.Equal(t, "com.app.F61.run", names[0])
	assert.Contains(t, names, "org.springframework.lib58.Run.go")
	assert.NotContains(t, names, "org.springframework.lib61.Run.go")
	assert.NotContains(t, names, "org.springframework.lib60.Run.go")
}

func TestRootFirstJavaFramesCapKeepsLeafMostApp(t *testing.T) {
	var in []string
	for i := 0; i < 80; i++ {
		in = append(in, fmt.Sprintf("com.app.F%d.run", i))
		if i%10 == 5 {
			in = append(in, fmt.Sprintf("org.springframework.lib%d.Run.go", i))
		}
	}
	names, types := rootFirstJavaFrames(in, jvmTypes(len(in)))
	assert.Len(t, names, 64)
	assert.Len(t, types, 64)
	// leaf-most 64 app frames (F0..F63), no library, root-first.
	assert.Equal(t, "com.app.F63.run", names[0])
	assert.Equal(t, "com.app.F0.run", names[63])
	for _, n := range names {
		assert.False(t, interrogationIsLibrary(n), n)
	}
}
