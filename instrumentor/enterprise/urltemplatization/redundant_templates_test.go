package urltemplatization

import (
	"reflect"
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/actions"
	commonlogger "github.com/odigos-io/odigos/common/logger"

	learning "github.com/odigos-io/odigos/common/urltemplatization/learning"
)

func templateSet(templates ...string) map[string]any {
	set := make(map[string]any, len(templates))
	for _, template := range templates {
		set[template] = nil
	}
	return set
}

func TestRedundantTemplates(t *testing.T) {
	got := redundantTemplates(templateSet(
		"/api/v1/namespaces/*/*",
		"/api/v1/namespaces/*/configmaps",
		"/api/v1/namespaces/*/events",
		"/api/v1/namespaces/default/endpoints/kubernetes",
		"/api/v1/namespaces/*/endpoints/kubernetes",
		"/api/v1/namespaces/*/endpoints/*",
		"/api/v1/namespaces/default/pods/{id}",
		"/api/v1/namespaces/*/pods/{id}",
		"/api/v1/namespaces/*/configmaps/effective-config",
		"/apis/*/v1/namespaces/*/*/{id}",
		"/apis/*/v1/namespaces/*/leases/kind-worker",
		"/apis/*/v1/namespaces/*/deployments/*",
		"/foo/*/bar",
		"/foo/baz/*",
	))
	want := templateSet(
		"/api/v1/namespaces/*/configmaps",
		"/api/v1/namespaces/*/events",
		"/api/v1/namespaces/default/endpoints/kubernetes",
		"/api/v1/namespaces/*/endpoints/kubernetes",
		"/api/v1/namespaces/default/pods/{id}",
	)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redundant = %v, want %v", got, want)
	}
}

func TestMergeLearnedTemplates_DropsRedundantTemplates(t *testing.T) {
	checkout := k8sconsts.PodWorkload{Namespace: "payments", Kind: k8sconsts.WorkloadKindDeployment, Name: "checkout"}
	job := NewLiveTrafficLearningJob(nil, commonlogger.LoggerCompat())
	existing := []actions.UrlTemplatizationDocumentedTemplate{
		{Template: "/users/admin"},
		{Template: "/orders/{id}"},
	}

	got, changed := job.mergeLearnedTemplates(checkout, existing, []learning.LearnedRule{
		{Template: "/users/*"},
		{Template: "/orders/*/items/list"},
		{Template: "/orders/*/items/*"},
	})
	templates := make([]string, 0, len(got))
	for _, documented := range got {
		templates = append(templates, documented.Template)
	}
	want := []string{"/orders/{id}", "/users/*", "/orders/*/items/*"}
	if !changed || !reflect.DeepEqual(templates, want) {
		t.Fatalf("changed=%v templates=%v, want %v", changed, templates, want)
	}
	if len(existing) != 2 || existing[0].Template != "/users/admin" {
		t.Fatalf("input slice was modified: %+v", existing)
	}

	_, changed = job.mergeLearnedTemplates(checkout, got, []learning.LearnedRule{{Template: "/users/guest"}})
	if changed {
		t.Fatal("redundant learned template marked the rule changed")
	}
}
