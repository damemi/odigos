package gqlapi

import (
	"errors"
	"testing"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorsettings"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestManagedCollectorErrorsHaveStableExtensions(t *testing.T) {
	for _, tc := range []struct{ message, code, field string }{
		{"region x already has a Collector", "COLLECTOR_EXISTS", ""},
		{"providerOverrides.aws.clusterRef must identify an ACTIVE ECS cluster", "INVALID_FIELD", "providerOverrides.aws.clusterRef"},
		{"connector credentials: operation error STS: AssumeRole", "AWS_CREDENTIALS_UNAVAILABLE", ""},
		{"region x has tearing-down cloud state", "TEARDOWN_IN_PROGRESS", ""},
		{"secretRef must be a Secrets Manager secret ARN", "INVALID_FIELD", "secretRef"},
		{"credentialId is required", "INVALID_FIELD", "credentialId"},
		{"scaling minCount cannot be negative", "INVALID_FIELD", "scaling.minCount"},
		{"transport mode needs a hostname", "INVALID_FIELD", "transport.mode"},
		{"the Collector already has a credential", "CREDENTIAL_EXISTS", ""},
		{"the Collector has no active credential to rotate", "CREDENTIAL_NOT_ACTIVE", ""},
		{"region x has no enabled managed Collector", "COLLECTOR_NOT_FOUND", ""},
	} {
		got, ok := TypedManagedCollectorError(errors.New(tc.message)).(*gqlerror.Error)
		if !ok || got.Extensions["code"] != tc.code || (tc.field != "" && got.Extensions["field"] != tc.field) {
			t.Fatalf("%q: got %#v", tc.message, got)
		}
	}
}

func TestCollectorEmptyListsClearPinnedSettings(t *testing.T) {
	patch := patchFromUpdateInput(model.UpdateManagedCollectorInput{
		ProviderOverrides: &model.ManagedCollectorProviderOverridesInput{
			AWS: &model.AWSManagedCollectorProviderOverridesInput{
				SubnetIds: []string{}, AdditionalSecurityGroupIds: []string{},
			},
		},
		Ingress: &model.ManagedCollectorIngressInput{AdditionalCidrs: []string{}},
	})
	if value, ok := patch.Provider["subnetIds"].([]any); !ok || value == nil || len(value) != 0 {
		t.Fatalf("explicit empty subnetIds lost: %#v", patch.Provider)
	}
	if patch.Ingress == nil || patch.Ingress.AdditionalCIDRs == nil ||
		patch.Ingress.AdditionalNetworkIdentityIDs == nil {
		t.Fatalf("explicit empty ingress lists lost: %#v", patch.Ingress)
	}
	merged := patch.MergeInto(map[string]any{
		collectorsettings.KeyProvider: map[string]any{"subnetIds": []any{"subnet-a"}},
		collectorsettings.KeyIngress: map[string]any{
			collectorsettings.KeyAdditionalCIDRs:              []any{"10.0.0.0/8"},
			collectorsettings.KeyAdditionalNetworkIdentityIDs: []any{"sg-old"},
		},
	})
	provider := merged[collectorsettings.KeyProvider].(map[string]any)
	if got := provider["subnetIds"].([]any); len(got) != 0 {
		t.Fatalf("subnet pin was not cleared: %v", got)
	}
	ingress := merged[collectorsettings.KeyIngress].(map[string]any)
	if len(ingress[collectorsettings.KeyAdditionalCIDRs].([]any)) != 0 ||
		len(ingress[collectorsettings.KeyAdditionalNetworkIdentityIDs].([]any)) != 0 {
		t.Fatalf("ingress pins were not cleared: %v", ingress)
	}
}

func TestLegacyDeploymentRoleIsIgnored(t *testing.T) {
	legacy := "arn:aws:iam::123456789012:role/old-collector-role"
	patch := patchFromUpdateInput(model.UpdateManagedCollectorInput{DeploymentRoleRef: &legacy})
	if patch.DeploymentRoleRef != nil {
		t.Fatalf("legacy role was still written to Collector settings: %+v", patch)
	}
}
