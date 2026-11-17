package loop

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
)

func TestInstrumentationConfigForSource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := odigosv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &odigosv1.InstrumentationConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-checkout"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	source := clickhouse.SourceKey{
		Namespace:    "shop",
		WorkloadKind: "Deployment",
		WorkloadName: "checkout",
	}
	got, err := instrumentationConfigForSource(context.Background(), c, source)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "deployment-checkout" {
		t.Fatalf("got %#v", got)
	}

	missing := source
	missing.WorkloadName = "other"
	got, err = instrumentationConfigForSource(context.Background(), c, missing)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("missing config: got %#v err %v", got, err)
	}

	got, err = instrumentationConfigForSource(context.Background(), client.Client(nil), source)
	if err != nil || got != nil {
		t.Fatalf("nil client: got %#v err %v", got, err)
	}
}
