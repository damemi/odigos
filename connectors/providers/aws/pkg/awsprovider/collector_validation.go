package awsprovider

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectoringress"
	basecollectorsettings "github.com/odigos-io/odigos/connectors/base/domain/collectorsettings"
	collectorsettings "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos/connectors/spec"
)

var _ baseapi.CollectorRegionalSettingsValidator = (*Provider)(nil)

// ValidateCollectorRegionalSettings checks the values a new ECS Collector needs
// before writing desired state. The caller supplies a sparse patch on update.
func (p *Provider) ValidateCollectorRegionalSettings(ctx context.Context, region string, patch basecollectorsettings.Patch, create bool) error {
	if patch.TLS != nil && patch.TLS.Mode != nil && collectoringress.TLSMode(*patch.TLS.Mode) == collectoringress.TLSCollector {
		return fmt.Errorf("transport.mode=collector is unavailable on AWS until Collector TLS material delivery is supported")
	}
	current := collectorsettings.Read(p.cfgMgr.Effective()).Region(region)
	cluster := current.ClusterRef
	if v, ok := patch.Provider[collectorsettings.KeyClusterRef].(string); ok {
		cluster = v
	}
	executionRole := current.ExecutionRoleARN
	if v, ok := patch.Provider[collectorsettings.KeyExecutionRoleARN].(string); ok {
		executionRole = v
	}
	network := current.HostNetworkID
	if patch.HostNetworkID != nil {
		network = *patch.HostNetworkID
	}
	if strings.TrimSpace(cluster) == "" {
		return fmt.Errorf("providerOverrides.aws.clusterRef is required")
	}
	if strings.TrimSpace(network) == "" {
		return fmt.Errorf("networkId is required to place the regional Collector")
	}
	parsed, err := arn.Parse(executionRole)
	if err != nil || parsed.Service != "iam" || parsed.AccountID != p.cfg.AccountID ||
		!strings.HasPrefix(parsed.Resource, "role/") {
		return fmt.Errorf("providerOverrides.aws.executionRoleArn must be an IAM role ARN in account %s", p.cfg.AccountID)
	}
	// Manual and automatic modes use the connector's configured AWS identity. Its
	// capability-scoped policy supplies reads in both modes and writes only in auto.
	cfg, err := p.cache.Config(ctx, region)
	if err != nil {
		return fmt.Errorf("resolve connector AWS credentials: %w", err)
	}
	clusters, err := ecs.NewFromConfig(cfg).DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{cluster}})
	if err != nil {
		return fmt.Errorf("providerOverrides.aws.clusterRef: %w", err)
	}
	if len(clusters.Clusters) != 1 || clusters.Clusters[0].Status == nil || *clusters.Clusters[0].Status != "ACTIVE" {
		return fmt.Errorf("providerOverrides.aws.clusterRef must identify an ACTIVE ECS cluster")
	}
	vpcs, err := ec2.NewFromConfig(cfg).DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{network}})
	if err != nil {
		return fmt.Errorf("networkId: %w", err)
	}
	if len(vpcs.Vpcs) != 1 || aws.ToString(vpcs.Vpcs[0].VpcId) != network {
		return fmt.Errorf("networkId must identify an existing VPC in %s", region)
	}
	subnets := current.SubnetIDs
	if values, ok := patch.Provider[collectorsettings.KeySubnetIDs].([]any); ok {
		subnets = subnets[:0]
		for _, value := range values {
			if id, ok := value.(string); ok {
				subnets = append(subnets, id)
			}
		}
	}
	if create && p.cfg.Capabilities.CollectorMode(collectorType) == spec.ModeManual && len(subnets) == 0 {
		return fmt.Errorf("providerOverrides.aws.subnetIds is required in manual mode; choose the Collector subnets before saving")
	}
	if len(subnets) > 0 {
		result, err := ec2.NewFromConfig(cfg).DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: subnets})
		if err != nil {
			return fmt.Errorf("providerOverrides.aws.subnetIds: %w", err)
		}
		if len(result.Subnets) != len(subnets) {
			return fmt.Errorf("providerOverrides.aws.subnetIds must identify existing subnets")
		}
		for _, subnet := range result.Subnets {
			if aws.ToString(subnet.VpcId) != network {
				return fmt.Errorf("providerOverrides.aws.subnetIds: subnet %s is outside networkId %s", aws.ToString(subnet.SubnetId), network)
			}
		}
	}
	securityGroups := current.SecurityGroupIDs
	if values, ok := patch.Provider[collectorsettings.KeySecurityGroupIDs].([]any); ok {
		securityGroups = securityGroups[:0]
		for _, value := range values {
			if id, ok := value.(string); ok {
				securityGroups = append(securityGroups, id)
			}
		}
	}
	if create && p.cfg.Capabilities.CollectorMode(collectorType) == spec.ModeManual && len(securityGroups) != 1 {
		return fmt.Errorf("providerOverrides.aws.securityGroupIds requires the one customer-owned Collector security group ID in manual mode")
	}
	if len(securityGroups) > 0 {
		result, err := ec2.NewFromConfig(cfg).DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: securityGroups})
		if err != nil {
			return fmt.Errorf("providerOverrides.aws.securityGroupIds: %w", err)
		}
		if len(result.SecurityGroups) != len(securityGroups) {
			return fmt.Errorf("providerOverrides.aws.securityGroupIds must identify existing security groups")
		}
		for _, group := range result.SecurityGroups {
			if aws.ToString(group.VpcId) != network {
				return fmt.Errorf("providerOverrides.aws.securityGroupIds: group %s is outside networkId %s", aws.ToString(group.GroupId), network)
			}
		}
	}
	return nil
}
