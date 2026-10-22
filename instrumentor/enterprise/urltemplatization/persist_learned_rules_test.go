package urltemplatization

import (
	"fmt"
	"slices"
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/actions"
	commonlogger "github.com/odigos-io/odigos/common/logger"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

func TestTemplatesByNamespace(t *testing.T) {
	checkout := k8sconsts.PodWorkload{Namespace: "payments", Kind: k8sconsts.WorkloadKindDeployment, Name: "checkout"}
	api := k8sconsts.PodWorkload{Namespace: "payments", Kind: k8sconsts.WorkloadKindDeployment, Name: "api"}
	web := k8sconsts.PodWorkload{Namespace: "shop", Kind: k8sconsts.WorkloadKindDeployment, Name: "web"}
	got := templatesByNamespace(map[k8sconsts.PodWorkload][]learning.LearnedRule{
		checkout: {{Template: "/users/{id}"}},
		api:      {{Template: "/orders/{id}"}},
		web:      {{Template: "/cart/{id}"}},
	})
	if len(got["payments"]) != 2 || len(got["payments"][checkout]) != 1 || got["payments"][checkout][0].Template != "/users/{id}" {
		t.Fatalf("payments checkout templates = %#v", got["payments"])
	}
	if len(got["payments"][api]) != 1 || got["payments"][api][0].Template != "/orders/{id}" {
		t.Fatalf("payments api templates = %#v", got["payments"][api])
	}
	if len(got["shop"]) != 1 || len(got["shop"][web]) != 1 || got["shop"][web][0].Template != "/cart/{id}" {
		t.Fatalf("shop templates = %#v", got["shop"])
	}
}

func TestAppendMissingDocumentedTemplates_CapsAt200(t *testing.T) {
	checkout := k8sconsts.PodWorkload{Namespace: "payments", Kind: k8sconsts.WorkloadKindDeployment, Name: "checkout"}
	existing := make([]actions.UrlTemplatizationDocumentedTemplate, maxDocumentedTemplatesPerScope)
	for i := range existing {
		existing[i] = actions.UrlTemplatizationDocumentedTemplate{Template: fmt.Sprintf("/existing/%d", i)}
	}
	job := NewLiveTrafficLearningJob(nil, commonlogger.LoggerCompat())
	got, changed := job.appendMissingDocumentedTemplates(checkout, existing, []learning.LearnedRule{{Template: "/new/{id}"}})
	if changed || len(got) != maxDocumentedTemplatesPerScope {
		t.Fatalf("changed=%v len=%d", changed, len(got))
	}

	existing = existing[:maxDocumentedTemplatesPerScope-1]
	got, changed = job.appendMissingDocumentedTemplates(checkout, existing, []learning.LearnedRule{
		{Template: "/new/{id}"},
		{Template: "/extra/{id}"},
	})
	if !changed || len(got) != maxDocumentedTemplatesPerScope || got[len(got)-1].Template != "/new/{id}" {
		t.Fatalf("changed=%v templates=%d last=%q", changed, len(got), got[len(got)-1].Template)
	}
}

func TestAppendMissingDocumentedTemplates_WritesExamples(t *testing.T) {
	checkout := k8sconsts.PodWorkload{Namespace: "payments", Kind: k8sconsts.WorkloadKindDeployment, Name: "checkout"}
	job := NewLiveTrafficLearningJob(nil, commonlogger.LoggerCompat())
	learned := []learning.LearnedRule{{
		Template: "/users/{id}",
		Reason:   "learned from traffic",
		Examples: []string{"/users/1", "/users/2"},
	}}

	got, changed := job.appendMissingDocumentedTemplates(checkout, nil, learned)
	if !changed || len(got) != 1 || got[0].Notes != "learned from traffic" || !slices.Equal(got[0].Examples, learned[0].Examples) {
		t.Fatalf("created template = %+v changed=%v", got, changed)
	}

	again, changed := job.appendMissingDocumentedTemplates(checkout, []actions.UrlTemplatizationDocumentedTemplate{{
		Template: "/users/{id}",
		Notes:    "learned from traffic",
	}}, learned)
	if !changed || !slices.Equal(again[0].Examples, learned[0].Examples) || again[0].Notes != "learned from traffic" {
		t.Fatalf("updated template = %+v changed=%v", again, changed)
	}

	_, changed = job.appendMissingDocumentedTemplates(checkout, again, learned)
	if changed {
		t.Fatal("unchanged examples marked the rule changed")
	}
}
