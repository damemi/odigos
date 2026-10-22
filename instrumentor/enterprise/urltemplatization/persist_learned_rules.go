package urltemplatization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/actions"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

const (
	liveTrafficActionNamePrefix = "ltl-auto-"
	// maxKubernetesNameLength is the Kubernetes limit for object names.
	maxKubernetesNameLength = 63
	// maxDocumentedTemplatesPerScope is the maximum number of documented templates stored on one workload rule.
	maxDocumentedTemplatesPerScope = 200
)

// persistLearnedRules writes learned templates onto a URL templatization Action,
// one Action per namespace. Each workload gets its own rule, scoped to that
// workload, containing only the templates learned for it. An existing rule for
// the workload is extended. Otherwise a new Action or rule is created.
func (j *LiveTrafficLearningJob) persistLearnedRules(ctx context.Context, learnedByWorkload map[k8sconsts.PodWorkload][]learning.LearnedRule) error {

	byNamespace := templatesByNamespace(learnedByWorkload)
	if len(byNamespace) == 0 {
		return nil
	}

	for namespace, byWorkload := range byNamespace {
		if err := j.persistNamespaceTemplates(ctx, namespace, byWorkload); err != nil {
			j.logger.Error("failed to persist learned templates", "namespace", namespace, "err", err)
		}
	}
	return nil
}

func (j *LiveTrafficLearningJob) persistNamespaceTemplates(ctx context.Context, namespace string, byWorkload map[k8sconsts.PodWorkload][]learning.LearnedRule) error {
	if len(byWorkload) == 0 {
		return nil
	}

	var actions odigosv1.ActionList
	if err := j.Client.List(ctx, &actions,
		client.InNamespace(env.GetCurrentNamespace()),
		client.MatchingLabels{
			k8sconsts.OdigosProfilesManagedByLabel: k8sconsts.OdigosLiveTrafficLearningManagedByValue,
			k8sconsts.WorkloadNamespaceLabel:       namespace,
		},
	); err != nil {
		return fmt.Errorf("list URL templatization actions: %w", err)
	}
	switch len(actions.Items) {
	case 0:
		action := j.newLiveTrafficTemplatizationAction(namespace, byWorkload)
		if err := j.Client.Create(ctx, action); err != nil {
			if apierrors.IsAlreadyExists(err) || apierrors.IsConflict(err) {
				j.logger.Info("URL templatization action already exists or conflicts; it will be handled in the next learning loop",
					"namespace", namespace,
					"action", action.Name,
					"err", err,
				)
				return nil
			}
			return fmt.Errorf("create action %s/%s: %w", action.Namespace, action.Name, err)
		}
		j.logger.Info("created URL templatization action from live traffic", "namespace", namespace, "action", action.Name)
		return nil
	case 1:
		action := &actions.Items[0]
		if action.Spec.URLTemplatization == nil {
			return fmt.Errorf("live-traffic URL templatization action %s/%s has no URL templatization config", action.Namespace, action.Name)
		}
		if !j.applyLearnedRules(action, byWorkload) {
			return nil
		}
		if err := j.Client.Update(ctx, action); err != nil {
			return fmt.Errorf("update action %s/%s: %w", action.Namespace, action.Name, err)
		}
		j.logger.Info("added learned URL templatization templates", "namespace", namespace, "action", action.Name)
		return nil
	default:
		return fmt.Errorf("expected 0 or 1 live-traffic URL templatization actions for namespace %s, found %d", namespace, len(actions.Items))
	}
}

// templatesByNamespace groups learned rules by namespace, keeping each workload's rules separate.
func templatesByNamespace(learnedByWorkload map[k8sconsts.PodWorkload][]learning.LearnedRule) map[string]map[k8sconsts.PodWorkload][]learning.LearnedRule {
	byNamespace := make(map[string]map[k8sconsts.PodWorkload][]learning.LearnedRule)
	for workload, rules := range learnedByWorkload {
		if workload.Namespace == "" || workload.Name == "" {
			continue
		}
		for _, rule := range rules {
			if byNamespace[workload.Namespace] == nil {
				byNamespace[workload.Namespace] = make(map[k8sconsts.PodWorkload][]learning.LearnedRule)
			}
			byNamespace[workload.Namespace][workload] = append(byNamespace[workload.Namespace][workload], rule)
		}
	}
	return byNamespace
}

// findWorkloadRule returns the rule scoped only to workload.
func findWorkloadRule(action *odigosv1.Action, workload k8sconsts.PodWorkload) *actions.UrlTemplatizationRule {
	if action.Spec.URLTemplatization == nil {
		return nil
	}
	for i := range action.Spec.URLTemplatization.Rules {
		rule := &action.Spec.URLTemplatization.Rules[i]
		if workloadOnlyScope(rule.Scopes, workload) {
			return rule
		}
	}
	return nil
}

func workloadOnlyScope(scopes *k8sconsts.SourcesScopes, workload k8sconsts.PodWorkload) bool {
	if scopes == nil || len(scopes.Namespaces) != 0 || len(scopes.Languages) != 0 {
		return false
	}
	return len(scopes.Sources) == 1 && scopes.Sources[0] == workload
}

func (j *LiveTrafficLearningJob) workloadRule(workload k8sconsts.PodWorkload, learned []learning.LearnedRule) actions.UrlTemplatizationRule {
	documented, _ := j.appendMissingDocumentedTemplates(workload, nil, learned)
	return actions.UrlTemplatizationRule{
		Scopes: &k8sconsts.SourcesScopes{
			Sources: []k8sconsts.PodWorkload{workload},
		},
		DocumentedTemplates: documented,
	}
}

func documentedTemplate(rule learning.LearnedRule) actions.UrlTemplatizationDocumentedTemplate {
	return actions.UrlTemplatizationDocumentedTemplate{
		Template: rule.Template,
		Notes:    strings.TrimSpace(rule.Reason),
	}
}

func (j *LiveTrafficLearningJob) appendMissingDocumentedTemplates(workload k8sconsts.PodWorkload, existing []actions.UrlTemplatizationDocumentedTemplate, add []learning.LearnedRule) ([]actions.UrlTemplatizationDocumentedTemplate, bool) {
	seen := make(map[string]struct{}, len(existing))
	for _, documented := range existing {
		seen[documented.Template] = struct{}{}
	}
	changed := false
	skipped := 0
	for _, rule := range add {
		if _, ok := seen[rule.Template]; ok {
			continue
		}
		if len(existing) >= maxDocumentedTemplatesPerScope {
			skipped++
			continue
		}
		seen[rule.Template] = struct{}{}
		existing = append(existing, documentedTemplate(rule))
		changed = true
	}
	if skipped > 0 {
		j.logger.Warn("skipped learned URL templatization templates over the per-scope cap",
			"namespace", workload.Namespace,
			"workload", workload.Name,
			"kind", workload.Kind,
			"cap", maxDocumentedTemplatesPerScope,
			"skipped", skipped,
		)
	}
	return existing, changed
}

// applyLearnedRules adds each workload's templates onto its existing rule, or appends a new rule.
// It reports whether the action changed.
func (j *LiveTrafficLearningJob) applyLearnedRules(action *odigosv1.Action, byWorkload map[k8sconsts.PodWorkload][]learning.LearnedRule) bool {
	changed := false
	for workload, learned := range byWorkload {
		existingRule := findWorkloadRule(action, workload)
		if existingRule != nil {
			updated, ruleChanged := j.appendMissingDocumentedTemplates(workload, existingRule.DocumentedTemplates, learned)
			if !ruleChanged {
				continue
			}
			existingRule.DocumentedTemplates = updated
			changed = true
			continue
		} else {
			action.Spec.URLTemplatization.Rules = append(action.Spec.URLTemplatization.Rules, j.workloadRule(workload, learned))
			changed = true
		}
	}
	return changed
}

func (j *LiveTrafficLearningJob) newLiveTrafficTemplatizationAction(namespace string, byWorkload map[k8sconsts.PodWorkload][]learning.LearnedRule) *odigosv1.Action {
	rules := make([]actions.UrlTemplatizationRule, 0, len(byWorkload))
	for workload, templates := range byWorkload {
		rules = append(rules, j.workloadRule(workload, templates))
	}
	return &odigosv1.Action{
		ObjectMeta: metav1.ObjectMeta{
			Name:      liveTrafficActionName(namespace),
			Namespace: env.GetCurrentNamespace(),
			Labels: map[string]string{
				k8sconsts.OdigosProfilesManagedByLabel: k8sconsts.OdigosLiveTrafficLearningManagedByValue,
				k8sconsts.WorkloadNamespaceLabel:       namespace,
			},
		},
		Spec: odigosv1.ActionSpec{
			ActionName: "Automatic URL templates (" + namespace + ")",
			Notes:      "Learned automatically from unmatched HTTP paths in live traffic for the " + namespace + " namespace. Odigos adds templates here as new rules are found.",
			Signals:    []common.ObservabilitySignal{common.TracesObservabilitySignal},
			URLTemplatization: &actions.URLTemplatizationConfig{
				Rules: rules,
			},
		},
	}
}

// liveTrafficActionName is "ltl-auto-<namespace>", where ltl-auto is the shortcut for
// live traffic learning automatic. For example, "ltl-auto-payments". Kubernetes object
// names are limited to maxKubernetesNameLength characters, so a longer namespace is truncated and
// suffixed with the first 8 hex characters of its SHA-256, for example
// "ltl-auto-very-long-application-namespace-name-for-chec-7f79bff2"
// (namespace "very-long-application-namespace-name-for-checkoutxextraextra").
func liveTrafficActionName(namespace string) string {
	name := liveTrafficActionNamePrefix + namespace
	if len(name) <= maxKubernetesNameLength {
		return name
	}
	sum := sha256.Sum256([]byte(namespace))
	suffix := hex.EncodeToString(sum[:4])
	keep := maxKubernetesNameLength - len(liveTrafficActionNamePrefix) - 1 - len(suffix)
	return liveTrafficActionNamePrefix + namespace[:keep] + "-" + suffix
}
