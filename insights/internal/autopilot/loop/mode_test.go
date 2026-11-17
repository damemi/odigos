package loop

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/consts"

	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
)

func TestReadAutopilotMode(t *testing.T) {
	t.Setenv(consts.CurrentNamespaceEnvVar, "odigos-system")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "odigos-system", Name: consts.OdigosEffectiveConfigName},
		Data: map[string]string{
			consts.OdigosConfigurationFileName: "autopilot:\n  enabled: true\n  mode: auto\n",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	mode, err := readAutopilotMode(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "auto" {
		t.Fatalf("mode = %q, want auto", mode)
	}

	mode, err = readAutopilotMode(context.Background(), client.Client(nil))
	if err != nil || mode != "" {
		t.Fatalf("nil client: mode %q err %v", mode, err)
	}
}

func TestProcessSourceModeOffSkipsAnalysis(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := odigosv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &odigosv1.InstrumentationConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-checkout"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	m := &Manager{Client: c}
	source := clickhouse.SourceKey{
		Namespace:    "shop",
		WorkloadKind: "Deployment",
		WorkloadName: "checkout",
	}

	if err := m.processSource(context.Background(), source, &sourceTickSummary{}, autopilotModeOff); err != nil {
		t.Fatal(err)
	}
}
