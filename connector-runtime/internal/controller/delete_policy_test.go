package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
)

func TestWantsCleanupOnDelete(t *testing.T) {
	cases := []struct {
		name string
		cr   *cloudconnectorsv1.OdigosCloudConnector
		want bool
	}{
		{
			name: "default retain",
			cr:   &cloudconnectorsv1.OdigosCloudConnector{},
			want: false,
		},
		{
			name: "explicit retain",
			cr: &cloudconnectorsv1.OdigosCloudConnector{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					annotationDeletionPolicy: "retain",
				}},
			},
			want: false,
		},
		{
			name: "cleanup annotation",
			cr: &cloudconnectorsv1.OdigosCloudConnector{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					annotationDeletionPolicy: deletionPolicyCleanup,
				}},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsCleanupOnDelete(tc.cr); got != tc.want {
				t.Fatalf("wantsCleanupOnDelete() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProviderImmutabilityViolated(t *testing.T) {
	crWith := func(lock, provider string) *cloudconnectorsv1.OdigosCloudConnector {
		cr := &cloudconnectorsv1.OdigosCloudConnector{Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{Provider: provider}}
		if lock != "" {
			cr.Annotations = map[string]string{annotationProviderLock: lock}
		}
		return cr
	}
	cases := []struct {
		name    string
		cr      *cloudconnectorsv1.OdigosCloudConnector
		violate bool
	}{
		{"no lock yet", crWith("", "aws"), false},
		{"lock matches", crWith("aws", "aws"), false},
		{"provider changed", crWith("aws", "gcp"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := providerImmutabilityViolated(tc.cr); got != tc.violate {
				t.Fatalf("providerImmutabilityViolated() = %v, want %v", got, tc.violate)
			}
		})
	}
}
