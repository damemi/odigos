package ruletests

import (
	"testing"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

// Mixed Kubernetes API traffic shaped like real apiserver calls: root probes,
// namespaced core/apps resources (pattern {id}), cluster-scoped RBAC, and
// metrics node scrapes. Word-like children have low template probability, so
// cardinality waits longer before "*" / "{id}".
func TestK8sAPIMultipleRuleKinds(t *testing.T) {
	paths := map[string]string{
		// Root / aggregation-layer style probes (exact).
		"/version": "101",
		"/readyz":  "101",
		"/healthz": "101",

		// Core v1 namespaced reads: GET .../namespaces/{ns}/pods/{name}
		"/api/v1/namespaces/1/pods/10": "40",
		"/api/v1/namespaces/2/pods/20": "40",
		"/api/v1/namespaces/3/pods/30": "40",

		// Pod log subresource: GET .../pods/{name}/log
		"/api/v1/namespaces/1/pods/10/log": "40",
		"/api/v1/namespaces/2/pods/20/log": "40",
		"/api/v1/namespaces/3/pods/30/log": "40",

		// Core v1 services: GET .../namespaces/{ns}/services/{name}
		"/api/v1/namespaces/1/services/10": "40",
		"/api/v1/namespaces/2/services/20": "40",
		"/api/v1/namespaces/3/services/30": "40",

		// Apps deployments: GET .../apis/apps/v1/namespaces/{ns}/deployments/{name}
		"/apis/apps/v1/namespaces/1/deployments/10": "40",
		"/apis/apps/v1/namespaces/2/deployments/20": "40",
		"/apis/apps/v1/namespaces/3/deployments/30": "40",

		// Batch jobs: GET .../apis/batch/v1/namespaces/{ns}/jobs/{name}
		"/apis/batch/v1/namespaces/1/jobs/10": "40",
		"/apis/batch/v1/namespaces/2/jobs/20": "40",
		"/apis/batch/v1/namespaces/3/jobs/30": "40",

		// Cluster-scoped RBAC: many non-pattern ClusterRole names → cardinality {id}.
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/sega": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segb": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segc": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segd": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/sege": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segf": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segg": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segh": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segi": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segj": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segk": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segl": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segm": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segn": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/sego": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segp": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segq": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segr": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segs": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segt": "5",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/segu": "5",

		// Metrics API node scrapes: non-pattern node names → wildcard *.
		"/apis/metrics.k8s.io/v1beta1/nodes/node-a": "20",
		"/apis/metrics.k8s.io/v1beta1/nodes/node-b": "20",
		"/apis/metrics.k8s.io/v1beta1/nodes/node-c": "20",
		"/apis/metrics.k8s.io/v1beta1/nodes/node-d": "20",
		"/apis/metrics.k8s.io/v1beta1/nodes/node-e": "20",
		"/apis/metrics.k8s.io/v1beta1/nodes/node-f": "20",
		"/apis/metrics.k8s.io/v1beta1/nodes/node-g": "20",
		"/apis/metrics.k8s.io/v1beta1/nodes/node-h": "20",
	}

	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(paths), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{
		"/api/v1/namespaces/{id}/pods/{id}",
		"/api/v1/namespaces/{id}/pods/{id}/log",
		"/api/v1/namespaces/{id}/services/{id}",
		"/apis/apps/v1/namespaces/{id}/deployments/{id}",
		"/apis/batch/v1/namespaces/{id}/jobs/{id}",
		"/apis/rbac.authorization.k8s.io/v1/clusterroles/*",
		"/healthz",
		"/readyz",
		"/version",
	})

	byTemplate := map[string]learning.LearnedRule{}
	for _, rule := range rules {
		byTemplate[rule.Template] = rule
	}

	assertSegments(t, byTemplate["/api/v1/namespaces/{id}/pods/{id}"].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "api"},
		{typ: learning.SegmentTypeStatic, name: "v1"},
		{typ: learning.SegmentTypeStatic, name: "namespaces"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
		{typ: learning.SegmentTypeStatic, name: "pods"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
	})
	assertSegments(t, byTemplate["/api/v1/namespaces/{id}/pods/{id}/log"].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "api"},
		{typ: learning.SegmentTypeStatic, name: "v1"},
		{typ: learning.SegmentTypeStatic, name: "namespaces"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
		{typ: learning.SegmentTypeStatic, name: "pods"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
		{typ: learning.SegmentTypeStatic, name: "log"},
	})
	assertSegments(t, byTemplate["/apis/apps/v1/namespaces/{id}/deployments/{id}"].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "apis"},
		{typ: learning.SegmentTypeStatic, name: "apps"},
		{typ: learning.SegmentTypeStatic, name: "v1"},
		{typ: learning.SegmentTypeStatic, name: "namespaces"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
		{typ: learning.SegmentTypeStatic, name: "deployments"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
	})
	assertSegments(t, byTemplate["/apis/rbac.authorization.k8s.io/v1/clusterroles/*"].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "apis"},
		{typ: learning.SegmentTypeStatic, name: "rbac.authorization.k8s.io"},
		{typ: learning.SegmentTypeStatic, name: "v1"},
		{typ: learning.SegmentTypeStatic, name: "clusterroles"},
		{typ: learning.SegmentTypeWildcard},
	})
}
