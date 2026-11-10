package k8s

import (
	"context"
	"testing"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUpdateCloudConnectorCollectorsClearsOnlySelection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := cloudconnectorsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	original := &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-account", Namespace: "odigos", Annotations: map[string]string{"odigos.io/collectors-capability": `[{"type":"aws.fargate-collector","management":true}]`}},
		Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{
			Provider: "aws", DisplayName: "Account",
			Collectors: []cloudconnectorsv1.CloudConnectorCollectorCapability{{Type: "aws.fargate-collector", Management: true, Deployment: true}},
		},
	}
	client := &Client{Ctrl: fake.NewClientBuilder().WithScheme(scheme).WithObjects(original).Build()}
	if err := client.UpdateCloudConnectorCollectors(context.Background(), "odigos", "aws-account", nil); err != nil {
		t.Fatal(err)
	}
	var got cloudconnectorsv1.OdigosCloudConnector
	if err := client.Ctrl.Get(context.Background(), ctrlclient.ObjectKey{Namespace: "odigos", Name: "aws-account"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Collectors) != 0 || got.Spec.Provider != "aws" || got.Spec.DisplayName != "Account" || got.Annotations["odigos.io/collectors-capability"] != "" {
		t.Fatalf("collector mode clear changed other fields: %+v", got.Spec)
	}
}
