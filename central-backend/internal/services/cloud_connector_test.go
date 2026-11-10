package services

import (
	"context"
	"testing"

	"github.com/odigos-io/central-backend/internal/k8s"
	cloudv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateConnectorPersistsBootstrapRegion(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := cloudv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ctrl := fake.NewClientBuilder().WithScheme(scheme).Build()
	svc := &CloudConnectorService{k8sClient: &k8s.Client{Ctrl: ctrl}, namespace: "odigos-central"}
	name, err := svc.CreateConnector(context.Background(), CreateConnectorParams{Provider: "aws", AccountID: "123456789012", BootstrapRegion: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	var got cloudv1.OdigosCloudConnector
	if err := ctrl.Get(context.Background(), client.ObjectKey{Namespace: "odigos-central", Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.BootstrapRegion != "eu-west-1" {
		t.Fatalf("persisted bootstrap region = %q", got.Spec.BootstrapRegion)
	}
}
