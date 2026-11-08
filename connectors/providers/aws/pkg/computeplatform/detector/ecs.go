// Package detector implements COMPUTE-PLATFORM discovery for the AWS connector — managed infra Odigos is
// installed on (ECS clusters today; future EKS/EC2). It enumerates clusters, classifies each as an
// ecs-cluster (EC2 capacity, agent installed) or fargate-cluster (runs Fargate tasks, coverage grouping),
// and persists the compute-platform store inventory the base reports as ComputePlatformReports. Workload (Lambda/Fargate
// task) discovery lives in the sibling workload/detector package.
package detector

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/computeplatform/store"
)

var log = connlog.Subsystem("aws-computeplatform-detector")

const describeClustersBatch = 100

// ecsFailureReasonMissing is the DescribeClusters failure reason AWS returns for a cluster that no longer
// exists (deleted between ListClusters and DescribeClusters). MISSING is genuinely gone, so letting the
// prune soft-delete it is correct; any other failure reason means the scan is incomplete and prune must
// be skipped.
const ecsFailureReasonMissing = "MISSING"

// ECSClusterDetector discovers ECS clusters and classifies them (EC2 capacity + Fargate presence). It is
// registered under both the ecs-cluster and fargate-cluster compute-platform discovery types (the base de-dups by
// identity, running the scan once per cycle).
type ECSClusterDetector struct {
	store *store.Store
	cache *awsclient.Cache
}

// NewECSClusterDetector builds the cluster detector over the compute-platform store and shared config cache.
func NewECSClusterDetector(st *store.Store, cache *awsclient.Cache) *ECSClusterDetector {
	return &ECSClusterDetector{store: st, cache: cache}
}

func (d *ECSClusterDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	regions, err := d.cache.EnabledRegions(ctx)
	if err != nil {
		return baseapi.DetectResult{}, fmt.Errorf("enumerate enabled regions: %w", err)
	}
	log.Info("ecs cluster detector starting", "regions", len(regions))
	var anyErr error
	authed := false
	for _, region := range regions {
		// The prune cutoff comes from the database clock, the one last_seen_at is written with.
		scanStart, err := d.store.Now(ctx)
		if err != nil {
			anyErr = fmt.Errorf("region %s: read database clock: %w", region, err)
			continue
		}
		cfg, err := d.cache.Config(ctx, region)
		if err != nil {
			anyErr = err
			continue
		}
		clusters, err := discoverClusters(ctx, cfg, region)
		if err != nil {
			anyErr = err
			log.Error("ecs cluster region scan failed", "region", region, "error", err)
			continue
		}
		authed = true
		upsertFailed := false
		for _, c := range clusters {
			if err := d.store.UpsertECSCluster(ctx, c); err != nil {
				log.Error("upsert ecs cluster failed", "region", region, "name", c.ClusterName, "error", err)
				upsertFailed = true
			}
		}
		_ = d.store.UpdateDiscoveryState(ctx, region, store.KindECSCluster, scanStart)
		_ = d.store.UpdateDiscoveryState(ctx, region, store.KindECSFargateCluster, scanStart)
		// Prune only when every discovered cluster persisted this cycle; pruning after a failed upsert would
		// soft-delete a still-live cluster whose last_seen_at wasn't refreshed this scan.
		if upsertFailed {
			anyErr = fmt.Errorf("region %s: one or more ecs cluster upserts failed; skipping prune", region)
			log.Error("skipping compute-platform prune after upsert failure", "region", region)
		} else if err := d.store.Prune(ctx, region, scanStart); err != nil {
			log.Error("prune compute-platform inventory failed", "region", region, "error", err)
		}
		log.Info("ecs cluster region scan complete", "region", region, "clusters", len(clusters))
	}
	return baseapi.DetectResult{Authenticated: authed}, anyErr
}

// discoverClusters enumerates clusters, fetches EC2 capacity (DescribeClusters), and records whether each
// runs Fargate tasks (a cheap Fargate-filtered ListTasks). Classification into ecs-cluster vs
// fargate-cluster happens at read time in the store from these two signals.
func discoverClusters(ctx context.Context, cfg aws.Config, region string) ([]store.ECSCluster, error) {
	log.Info("ecs cluster discovery starting", "region", region)
	client := ecs.NewFromConfig(cfg)

	var clusterARNs []string
	clusterPaginator := ecs.NewListClustersPaginator(client, &ecs.ListClustersInput{})
	for pageNum := 1; clusterPaginator.HasMorePages(); pageNum++ {
		page, err := clusterPaginator.NextPage(ctx)
		if err != nil {
			log.Error("ecs list clusters failed", "region", region, "page", pageNum, "error", err)
			return nil, err
		}
		clusterARNs = append(clusterARNs, page.ClusterArns...)
	}

	var out []store.ECSCluster
	for _, batch := range chunk(clusterARNs, describeClustersBatch) {
		desc, err := client.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: batch})
		if err != nil {
			log.Error("ecs describe clusters failed", "region", region, "error", err)
			return nil, err
		}
		// A non-MISSING failure means this batch is incomplete: return an error so the region's prune is
		// skipped rather than soft-deleting live clusters we just failed to describe.
		for _, f := range desc.Failures {
			if !strings.EqualFold(aws.ToString(f.Reason), ecsFailureReasonMissing) {
				return nil, fmt.Errorf("describe clusters incomplete in region %s: arn=%s reason=%s detail=%s",
					region, aws.ToString(f.Arn), aws.ToString(f.Reason), aws.ToString(f.Detail))
			}
		}
		for _, c := range desc.Clusters {
			arn := aws.ToString(c.ClusterArn)
			hasFargate, err := clusterRunsFargate(ctx, client, region, arn)
			if err != nil {
				return nil, err
			}
			out = append(out, store.ECSCluster{
				Region:                 region,
				ClusterARN:             arn,
				ClusterName:            aws.ToString(c.ClusterName),
				Status:                 aws.ToString(c.Status),
				RegisteredEC2Instances: c.RegisteredContainerInstancesCount,
				HasFargate:             hasFargate,
				Metadata: map[string]any{
					"runningTasksCount":   c.RunningTasksCount,
					"activeServicesCount": c.ActiveServicesCount,
				},
			})
		}
	}
	log.Info("ecs cluster discovery complete", "region", region, "clusters", len(out))
	return out, nil
}

// clusterRunsFargate reports whether a cluster runs at least one Fargate task, using a Fargate-filtered
// ListTasks (no DescribeTasks needed — task details belong to the workload domain).
func clusterRunsFargate(ctx context.Context, client *ecs.Client, region, clusterARN string) (bool, error) {
	paginator := ecs.NewListTasksPaginator(client, &ecs.ListTasksInput{
		Cluster:    aws.String(clusterARN),
		LaunchType: ecstypes.LaunchTypeFargate,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			log.Error("ecs list fargate tasks failed", "region", region, "cluster", clusterARN, "error", err)
			return false, err
		}
		if len(page.TaskArns) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func chunk[T any](xs []T, size int) [][]T {
	if size <= 0 || len(xs) == 0 {
		if len(xs) == 0 {
			return nil
		}
		return [][]T{xs}
	}
	var out [][]T
	for i := 0; i < len(xs); i += size {
		end := i + size
		if end > len(xs) {
			end = len(xs)
		}
		out = append(out, xs[i:end])
	}
	return out
}
