package collectorsettings

import (
	"reflect"
	"testing"

	base "github.com/odigos-io/odigos/connectors/base/domain/collectorsettings"
)

func TestExecutionRoleARNIsReadFromProviderSettings(t *testing.T) {
	want := "arn:aws:iam::123456789012:role/OdigosCollectorExecutionRole"
	got := FromBase(base.Settings{Defaults: base.Network{
		Provider: map[string]any{KeyExecutionRoleARN: want},
	}}).Defaults.ExecutionRoleARN
	if got != want {
		t.Fatalf("execution role ARN = %q, want %q", got, want)
	}
}

func TestAWSSettingsComeOnlyFromProviderMap(t *testing.T) {
	settings := Read(map[string]any{base.Key: map[string]any{
		base.KeyEnabled: true,
		base.KeyDefaults: map[string]any{
			base.KeyProvider: map[string]any{
				KeyClusterRef:       "arn:aws:ecs:eu-west-1:1:cluster/default",
				KeyAssignPublicIP:   false,
				KeyUseFargateSpot:   true,
				KeySecurityGroupIDs: []any{"sg-default"},
			},
		},
		base.KeyRegions: map[string]any{
			"eu-west-1": map[string]any{
				base.KeyProvider: map[string]any{
					KeySubnetIDs:        []any{"subnet-a", "subnet-b"},
					KeyAssignPublicIP:   true,
					KeySecurityGroupIDs: []any{},
				},
			},
		},
	}})

	region := settings.Region("eu-west-1")
	if region.ClusterRef != "arn:aws:ecs:eu-west-1:1:cluster/default" || !region.AssignPublicIP || !region.UseFargateSpot {
		t.Fatalf("AWS settings = %+v", region.Network)
	}
	if !reflect.DeepEqual(region.SubnetIDs, []string{"subnet-a", "subnet-b"}) {
		t.Fatalf("subnets = %v", region.SubnetIDs)
	}
	if len(region.SecurityGroupIDs) != 0 {
		t.Fatalf("explicitly cleared security groups = %v", region.SecurityGroupIDs)
	}
}

func TestBaseProjectionContainsNoAWSPlacementFields(t *testing.T) {
	settings := Read(map[string]any{base.Key: map[string]any{
		base.KeyEnabled: true,
		base.KeyRegions: map[string]any{"eu-west-1": map[string]any{
			base.KeyHostNetworkID: "vpc-1",
			base.KeyProvider:      map[string]any{KeySubnetIDs: []any{"subnet-a"}},
		}},
	}})

	projected := settings.Base().Region("eu-west-1")
	if projected.HostNetworkID != "vpc-1" || projected.Provider[KeySubnetIDs] == nil {
		t.Fatalf("base projection lost provider-neutral data: %+v", projected)
	}
}
