package interrogation

import "testing"

func TestClassifyFrame(t *testing.T) {
	cases := map[string]FrameKind{
		"":                                FrameKindRuntime,
		"vtable chunks":                   FrameKindRuntime,
		"StubRoutines::call_stub":         FrameKindRuntime,
		"JavaThread::thread_main_inner()": FrameKindRuntime,
		"java.lang.Object java.util.HashMap.get(java.lang.Object)":                      FrameKindRuntime,
		"void jdk.internal.reflect.DirectMethodHandleAccessor.invoke(java.lang.Object)": FrameKindRuntime,
		"void io.opentelemetry.javaagent.shaded.Foo.bar()":                              FrameKindRuntime,

		// Odigos Java agent frames seen inside app methods on a live cluster.
		"byte[] dev.keyval.net.bytebuddy.agent.builder.AgentBuilder$Default$ExecutingTransformer.transform(java.lang.Object, java.lang.ClassLoader)": FrameKindRuntime,
		"void io.odigos.kafka.producer.KafkaProducerV3Instrumentation$PartitionerAdvice.recordBrokerHost(java.lang.String)":                          FrameKindRuntime,
		"void io.odigos.spring.servlet.SpringServletAdvice.detach(long)":                                                                             FrameKindRuntime,
		"void dev.keyval.ebpfJava.utils.ThreadContext.restoreContext(long)":                                                                          FrameKindRuntime,
		"void dev.keyval.ebpf.shaded.protobuf.CodedOutputStream.write(byte)":                                                                         FrameKindRuntime,

		"io.jsonwebtoken.Jws io.jsonwebtoken.impl.DefaultJwtParser.parseSignedClaims(java.lang.CharSequence)":                                                    FrameKindLibrary,
		"java.lang.Object com.fasterxml.jackson.databind.ObjectMapper.readValue(byte[], java.lang.Class)":                                                        FrameKindLibrary,
		"void org.jboss.logging.DelegatingBasicLogger.tracef(java.lang.String, java.lang.Object)":                                                                FrameKindLibrary,
		"void io.reactivex.rxjava3.internal.schedulers.ScheduledRunnable.run()":                                                                                  FrameKindLibrary,
		"java.lang.Object com.github.benmanes.caffeine.cache.BoundedLocalCache.computeIfAbsent(java.lang.Object, java.util.function.Function, boolean, boolean)": FrameKindLibrary,

		"java.lang.Object org.springframework.data.repository.core.support.RepositoryMethodInvoker.invoke(java.lang.Object, java.lang.reflect.Method, java.lang.Object[])": FrameKindLibrary,
		"java.lang.Object org.hibernate.internal.SessionImpl.find(java.lang.Class, java.lang.Object, java.util.Map)":                                                       FrameKindLibrary,
		"void org.apache.kafka.clients.consumer.KafkaConsumer.poll(java.time.Duration)":                                                                                    FrameKindLibrary,
		"void org.apache.tomcat.util.threads.ThreadPoolExecutor.runWorker(org.apache.tomcat.util.threads.ThreadPoolExecutor$Worker)":                                       FrameKindLibrary,
		"okhttp3.Response okhttp3.internal.connection.RealCall.execute()":                                                                                                  FrameKindLibrary,

		"org.springframework.http.ResponseEntity com.rowan.notify.DocumentController.create(com.rowan.notify.DocumentRequest)": FrameKindApp,
		"void com.rowan.notify.RenderMessageProducer.publish(java.lang.String, com.rowan.notify.DocumentRequest)":              FrameKindApp,
		"com.rowan.notify.Controller.entry": FrameKindApp,
		// Customer code under dev.keyval (kv-mall) is not the agent.
		"void dev.keyval.kvshop.frontend.ProductController.buyProduct(int)": FrameKindApp,
	}
	for name, want := range cases {
		if got := ClassifyFrame(name); got != want {
			t.Errorf("ClassifyFrame(%q) = %q, want %q", name, got, want)
		}
		if got := IsApplicationFrame(name); got != (want == FrameKindApp) {
			t.Errorf("IsApplicationFrame(%q) = %v", name, got)
		}
		if got := IsRuntimeFrame(name); got != (want == FrameKindRuntime) {
			t.Errorf("IsRuntimeFrame(%q) = %v", name, got)
		}
	}
}
