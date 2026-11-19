package autopilot

import "testing"

func TestClassifyFrame(t *testing.T) {
	cases := map[string]FrameKind{
		"":                                FrameKindRuntime,
		"vtable chunks":                   FrameKindRuntime,
		"StubRoutines::call_stub":         FrameKindRuntime,
		"JavaThread::thread_main_inner()": FrameKindRuntime,
		"java.lang.Object java.util.HashMap.get(java.lang.Object)":                          FrameKindRuntime,
		"void jdk.internal.reflect.DirectMethodHandleAccessor.invoke(java.lang.Object)":     FrameKindRuntime,
		"void io.opentelemetry.javaagent.shaded.Foo.bar()":                                  FrameKindRuntime,
		"boolean dev.keyval.net.bytebuddy.matcher.ErasureMatcher.doMatch(java.lang.Object)": FrameKindInstrumentation,
		"void dev.keyval.ebpfJava.LibraryActivation.activate(int)":                          FrameKindInstrumentation,
		"void io.odigos.spring.servlet.SpringServletAdvice.detach(long)":                    FrameKindInstrumentation,
		"java.lang.String com.app.web.AppAdvice.funcEnter(java.lang.String, long)":          FrameKindInstrumentation,
		"void com.app.web.AppAdvice.funcExit(int, byte[], byte[])":                          FrameKindInstrumentation,
		// Shares the agent's dev.keyval root, but is an application (simple-demo's frontend).
		"void dev.keyval.kvshop.frontend.ProductController.buyProduct(int)": FrameKindApp,

		"java.lang.Object org.springframework.data.repository.core.support.RepositoryMethodInvoker.invoke(java.lang.Object, java.lang.reflect.Method, java.lang.Object[])": FrameKindApp,
		"java.lang.Object org.hibernate.internal.SessionImpl.find(java.lang.Class, java.lang.Object, java.util.Map)":                                                       FrameKindApp,
		"void org.apache.kafka.clients.consumer.KafkaConsumer.poll(java.time.Duration)":                                                                                    FrameKindApp,
		"void org.apache.tomcat.util.threads.ThreadPoolExecutor.runWorker(org.apache.tomcat.util.threads.ThreadPoolExecutor$Worker)":                                       FrameKindApp,
		"okhttp3.Response okhttp3.internal.connection.RealCall.execute()":                                                                                                  FrameKindApp,
		"com.fasterxml.jackson.databind.JsonDeserializer com.fasterxml.jackson.databind.deser.BeanDeserializerFactory.createBeanDeserializer(com.fasterxml.jackson.databind.DeserializationContext, com.fasterxml.jackson.databind.JavaType, com.fasterxml.jackson.databind.BeanDescription)": FrameKindApp,

		"org.springframework.http.ResponseEntity com.rowan.notify.DocumentController.create(com.rowan.notify.DocumentRequest)": FrameKindApp,
		"void com.rowan.notify.RenderMessageProducer.publish(java.lang.String, com.rowan.notify.DocumentRequest)":              FrameKindApp,
		"com.rowan.account.model.Account com.rowan.account.repo.AccountLookup.loadById(java.lang.String)":                      FrameKindApp,
		"com.rowan.notify.Controller.entry": FrameKindApp,
	}
	for name, want := range cases {
		if got := ClassifyFrame(name); got != want {
			t.Errorf("ClassifyFrame(%q) = %q, want %q", name, got, want)
		}
	}
}
