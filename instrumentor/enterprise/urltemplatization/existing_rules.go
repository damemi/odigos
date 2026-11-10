package urltemplatization

import (
	"context"
	"fmt"
	"strings"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/k8sconsts"
	actionsapi "github.com/odigos-io/odigos/common/api/actions"
	"github.com/odigos-io/odigos/common/urltemplate"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// loadExistingPathRules reads accepted URL templatization templates from the
// workload's InstrumentationConfig (collector WorkloadCollectorConfig, else agent Traces).
// Rules are keyed by segment count, matching the urltemplate processor index.
func loadExistingPathRules(ctx context.Context, c client.Client, w k8sconsts.PodWorkload) (map[int][]urltemplate.PathRule, error) {
	if c == nil {
		return nil, nil
	}

	icName := workload.CalculateWorkloadRuntimeObjectName(w.Name, w.Kind)
	var ic odigosv1.InstrumentationConfig
	err := c.Get(ctx, client.ObjectKey{Namespace: w.Namespace, Name: icName}, &ic)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get instrumentationconfig %s/%s: %w", w.Namespace, icName, err)
	}

	return urltemplate.ParseRulesBySegmentCount(collectTemplatesFromIC(&ic)), nil
}

func collectTemplatesFromIC(ic *odigosv1.InstrumentationConfig) []string {
	byContainer := collectUrlTemplatizationConfigsByContainer(ic)
	seen := make(map[string]struct{})
	var templates []string
	for _, cfg := range byContainer {
		if cfg == nil {
			continue
		}
		for _, template := range cfg.Templates {
			template = strings.TrimSpace(template)
			if template == "" {
				continue
			}
			if _, ok := seen[template]; ok {
				continue
			}
			seen[template] = struct{}{}
			templates = append(templates, template)
		}
	}
	return templates
}

func collectUrlTemplatizationConfigsByContainer(ic *odigosv1.InstrumentationConfig) map[string]*actionsapi.UrlTemplatizationConfig {
	byContainer := make(map[string]*actionsapi.UrlTemplatizationConfig)

	for i := range ic.Spec.WorkloadCollectorConfig {
		c := &ic.Spec.WorkloadCollectorConfig[i]
		if c.UrlTemplatization != nil {
			byContainer[c.ContainerName] = c.UrlTemplatization
		}
	}
	for i := range ic.Spec.Containers {
		c := &ic.Spec.Containers[i]
		if _, ok := byContainer[c.ContainerName]; ok {
			continue
		}
		if c.Traces != nil && c.Traces.UrlTemplatization != nil {
			byContainer[c.ContainerName] = c.Traces.UrlTemplatization
		}
	}
	return byContainer
}
