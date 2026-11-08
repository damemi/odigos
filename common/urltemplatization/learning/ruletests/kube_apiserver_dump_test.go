package ruletests

import (
	_ "embed"
	"encoding/json"
	"testing"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

//go:embed testdata/kube-apiserver-kind-control-plane-unmatched-server.json
var kubeApiserverUnmatchedServerJSON []byte

func TestKubeApiserverUnmatchedServerDump(t *testing.T) {
	var paths map[string]string
	if err := json.Unmarshal(kubeApiserverUnmatchedServerJSON, &paths); err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("expected non-empty path dump")
	}

	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(paths), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{
		"/api/v1/namespaces/*/configmaps",
		"/api/v1/namespaces/*/configmaps/odigos-data-collection",
		"/api/v1/namespaces/*/configmaps/odigos-deployment",
		"/api/v1/namespaces/*/configmaps/odigos-node-collector-config-domains",
		"/api/v1/namespaces/*/events",
		"/api/v1/namespaces/*/events/{id}",
		"/api/v1/namespaces/*/limitranges",
		"/api/v1/namespaces/*/pods",
		"/api/v1/namespaces/*/pods/{id}",
		"/api/v1/namespaces/*/pods/{id}/status",
		"/api/v1/namespaces/*/serviceaccounts/*/token",
		"/api/v1/namespaces/*/services/odigos-data-collection-local-traffic",
		"/api/v1/namespaces/*/services/odigos-gateway",
		"/apis/*/v1/namespaces/default/jobs/{id}/status",
		"/apis/*/v1/namespaces/kube-node-lease/leases/kind-control-plane",
		"/apis/*/v1/namespaces/kube-node-lease/leases/kind-worker",
		"/apis/*/v1/namespaces/odigos-system/deployments/odigos-gateway",
		"/apis/*/v1alpha1/namespaces/*/actions",
		"/apis/*/v1alpha1/namespaces/*/collectorsgroups/odigos-data-collection/status",
		"/apis/*/v1alpha1/namespaces/*/collectorsgroups/odigos-gateway/status",
		"/apis/*/v1alpha1/namespaces/*/instrumentationconfigs/*",
		"/apis/*/v1alpha1/namespaces/*/instrumentationconfigs/*/status",
		"/apis/*/v1alpha1/namespaces/*/recommendations/go-offset-updater",
		"/apis/*/v1alpha1/namespaces/*/recommendations/infer-db-attributes",
		"/apis/*/v1alpha1/namespaces/*/recommendations/odigos-metrics",
		"/apis/*/v1alpha1/namespaces/*/recommendations/sample-health-probes",
		"/apis/*/v1alpha1/namespaces/*/recommendations/url-templatization",
	})
}
