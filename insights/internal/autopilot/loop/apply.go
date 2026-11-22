package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

const autopilotRuleLabelKey = "odigos.io/autopilot-workload"

// defaultMaxProbesPerWorkload matches the Helm chart default when the env is unset.
const defaultMaxProbesPerWorkload = 20

func probeSlotsAvailable(existing []customProbe, maxProbes int) int {
	free := maxProbes - uniqueProbeCount(existing)
	if free < 0 {
		return 0
	}
	return free
}

func workloadScopes(source store.SourceKey) *k8sconsts.SourcesScopes {
	return &k8sconsts.SourcesScopes{
		Sources: []k8sconsts.PodWorkload{{
			Name:      source.WorkloadName,
			Namespace: source.Namespace,
			Kind:      k8sconsts.WorkloadKind(source.WorkloadKind),
		}},
	}
}

var dnsLabelSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

// labelValueSanitizer keeps only characters allowed in Kubernetes label values.
var labelValueSanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func autopilotRuleObjectName(source store.SourceKey) string {
	const prefix = "odig-int-"
	ns := dnsLabelSanitizer.ReplaceAllString(strings.ToLower(source.Namespace), "-")
	wl := dnsLabelSanitizer.ReplaceAllString(strings.ToLower(source.WorkloadName), "-")
	name := strings.Trim(prefix+ns+"-"+wl, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	if len(name) <= 63 && len(name) > len(prefix) {
		return name
	}
	h := sha256.Sum256([]byte(source.Namespace + "\x00" + source.WorkloadKind + "\x00" + source.WorkloadName))
	return prefix + hex.EncodeToString(h[:8])
}

func autopilotWorkloadRuleName(source store.SourceKey) string {
	return fmt.Sprintf("autopilot-%s", source.WorkloadName)
}

func autopilotRuleLabels(source store.SourceKey) map[string]string {
	return map[string]string{
		k8sconsts.OdigosProfilesManagedByLabel: k8sconsts.OdigosAutopilotLoopManagedByValue,
		autopilotRuleLabelKey:                  workloadLabelValue(source),
	}
}

func workloadLabelValue(source store.SourceKey) string {
	parts := []string{source.Namespace, source.WorkloadKind, source.WorkloadName}
	var out []string
	for _, p := range parts {
		p = labelValueSanitizer.ReplaceAllString(p, "-")
		p = strings.Trim(p, "-_.")
		if p == "" {
			p = "x"
		}
		out = append(out, p)
	}
	s := strings.Join(out, ".")
	if len(s) > 63 {
		h := sha256.Sum256([]byte(source.Namespace + "\x00" + source.WorkloadKind + "\x00" + source.WorkloadName))
		return "w" + hex.EncodeToString(h[:15])
	}
	return s
}

func skipExistingProbes(recs []schema.Recommendation, existing []customProbe) []schema.Recommendation {
	if len(existing) == 0 {
		return recs
	}
	have := make(map[string]bool, len(existing))
	for _, p := range existing {
		have[p.Language+"|"+p.Target] = true
	}
	var out []schema.Recommendation
	for _, rec := range recs {
		target := probeTarget(rec)
		if target != "" && have[rec.Language+"|"+target] {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func probeTarget(rec schema.Recommendation) string {
	switch rec.Language {
	case schema.LangJava:
		return rec.Probe.ClassName + "." + rec.Probe.MethodName
	case schema.LangGo:
		if rec.Probe.FunctionName != "" {
			return rec.Probe.PackageName + "." + rec.Probe.FunctionName
		}
		return rec.Probe.PackageName + ".(*" + rec.Probe.ReceiverName + ")." + rec.Probe.ReceiverMethodName
	case schema.LangPHP:
		if rec.Probe.ClassName == "" {
			return rec.Probe.FunctionName
		}
		return rec.Probe.ClassName + "::" + rec.Probe.FunctionName
	case schema.LangCpp:
		return rec.Probe.Signature
	default:
		return rec.Function
	}
}
