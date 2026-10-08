package deployer

import (
	"context"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

const (
	capacityProviderFargate     = "FARGATE"
	capacityProviderFargateSpot = "FARGATE_SPOT"
)

// ClusterClient is deliberately read-only. The customer provisions and owns the ECS cluster; the
// Collector installer only verifies the configured cluster before registering its task definition.
type clusterReader interface {
	DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error)
}

type ClusterClient interface{ clusterReader }

// ClusterInput is what validating the customer-provisioned regional cluster depends on.
type ClusterInput struct {
	Region string
	Mode   string
	// Ref is the required collector.regions.<region>.provider.clusterArn setting. Odigos uses this
	// cluster but never creates, tags, changes, or deletes it.
	Ref string
	// SpotWanted reports a missing FARGATE_SPOT association without mutating the cluster.
	SpotWanted bool
}

// Cluster is the customer-owned cluster one region's Collector runs in.
type Cluster struct {
	ARN  string
	Name string
	// CustomerNamed remains explicit because service and teardown code use it as an ownership boundary.
	// It is always true: automatic Collector deployment no longer owns ECS clusters.
	CustomerNamed     bool
	CapacityProviders []string
}

func (c Cluster) SupportsSpot() bool {
	return containsStringValue(c.CapacityProviders, capacityProviderFargateSpot)
}

// EnsureCluster validates the required, pre-provisioned ECS cluster without mutating it.
func EnsureCluster(ctx context.Context, api ClusterClient, in ClusterInput) (Cluster, []collectorstatus.Problem, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return Cluster{}, problems, nil
	}
	if in.Ref == "" {
		return Cluster{}, []collectorstatus.Problem{{
			Code:     ProblemClusterRequired,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector is deployed in %s: collector.regions.<region>.provider.clusterArn "+
				"is required. Provision an ECS cluster first and set clusterArn to its name or ARN; Odigos "+
				"installs the Fargate workload into that cluster but never creates or changes the cluster itself", in.Region),
		}}, nil
	}

	found, problems, err := describeCluster(ctx, api, in.Ref)
	if err != nil {
		return Cluster{}, problems, err
	}
	if found == nil {
		return Cluster{}, []collectorstatus.Problem{{
			Code:     ProblemClusterMissing,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("collector.regions.<region>.provider.clusterArn names %q, which is not an active "+
				"ECS cluster Odigos can find in %s. Provision the cluster first or correct clusterArn; Odigos "+
				"never creates or repairs the cluster", in.Ref, in.Region),
		}}, nil
	}
	out := clusterOf(found)
	return out, append(problems, spotProblems(in, out)...), nil
}

func spotProblems(in ClusterInput, c Cluster) []collectorstatus.Problem {
	if !in.SpotWanted || c.SupportsSpot() {
		return nil
	}
	return []collectorstatus.Problem{{
		Code:     ProblemSpotUnavailable,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("useFargateSpot is set for a Collector in %s but the customer-provisioned cluster %q "+
			"does not have the %s capacity provider associated, so it runs on on-demand Fargate instead. "+
			"Associate %s with the cluster to get the saving — and note what it costs: a Spot interruption "+
			"is a Collector restart, and a Collector on ephemeral storage loses its queue",
			in.Region, c.Name, capacityProviderFargateSpot, capacityProviderFargateSpot),
	}}
}

// describeCluster resolves one active cluster by name or ARN. ECS reports a missing cluster through
// failures rather than as an API error, so an empty active result means not found.
func describeCluster(ctx context.Context, api clusterReader, ref string) (*ecstypes.Cluster, []collectorstatus.Problem, error) {
	out, err := api.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{ref},
		Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldTags},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("describe ECS cluster %s: %w", ref, err)
	}
	for i := range out.Clusters {
		if aws.ToString(out.Clusters[i].Status) == clusterStatusActive {
			return &out.Clusters[i], nil, nil
		}
	}
	return nil, nil, nil
}

const clusterStatusActive = "ACTIVE"

func clusterOf(c *ecstypes.Cluster) Cluster {
	providers := append([]string(nil), c.CapacityProviders...)
	sort.Strings(providers)
	return Cluster{
		ARN:               aws.ToString(c.ClusterArn),
		Name:              aws.ToString(c.ClusterName),
		CustomerNamed:     true,
		CapacityProviders: providers,
	}
}

func ecsTagMap(tags []ecstypes.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

func ecsTags(tags map[string]string) []ecstypes.Tag {
	out := make([]ecstypes.Tag, 0, len(tags))
	for _, k := range sortedKeys(tags) {
		out = append(out, ecstypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}
