package interrogation

import "testing"

func TestClassifyFrameLibraryAndRuntime(t *testing.T) {
	library := []string{
		"java.lang.Object org.springframework.data.repository.core.support.RepositoryMethodInvoker.invoke(java.lang.Object, java.lang.reflect.Method, java.lang.Object[])",
		"java.lang.Object org.hibernate.internal.SessionImpl.find(java.lang.Class, java.lang.Object, java.util.Map)",
		"void org.apache.kafka.clients.consumer.KafkaConsumer.poll(java.time.Duration)",
		"void org.apache.tomcat.util.threads.ThreadPoolExecutor.runWorker(org.apache.tomcat.util.threads.ThreadPoolExecutor$Worker)",
	}
	for _, name := range library {
		if ClassifyFrame(name) != FrameKindLibrary {
			t.Fatalf("expected library frame %q, got %q", name, ClassifyFrame(name))
		}
		if IsRuntimeFrame(name) || IsApplicationFrame(name) {
			t.Fatalf("library frame %q must be neither runtime nor application", name)
		}
	}
	runtime := []string{
		"java.lang.Object java.util.HashMap.get(java.lang.Object)",
		"vtable chunks",
	}
	for _, name := range runtime {
		if !IsRuntimeFrame(name) || ClassifyFrame(name) != FrameKindRuntime {
			t.Fatalf("expected runtime frame %q", name)
		}
		if IsApplicationFrame(name) {
			t.Fatalf("expected not application frame %q", name)
		}
	}
}

func TestIsApplicationFrameRowan(t *testing.T) {
	app := []string{
		"org.springframework.http.ResponseEntity com.rowan.notify.DocumentController.create(com.rowan.notify.DocumentRequest)",
		"void com.rowan.notify.RenderMessageProducer.publish(java.lang.String, com.rowan.notify.DocumentRequest)",
		"com.rowan.account.model.Account com.rowan.account.repo.AccountLookup.loadById(java.lang.String)",
		"com.rowan.notify.Controller.entry",
	}
	for _, name := range app {
		if IsRuntimeFrame(name) {
			t.Fatalf("expected app frame %q", name)
		}
		if !IsApplicationFrame(name) {
			t.Fatalf("expected application frame %q", name)
		}
	}
}
