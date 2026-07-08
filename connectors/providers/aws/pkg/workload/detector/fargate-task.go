package detector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

const describeTasksBatch = 100

// ecsFailureReasonMissing is the DescribeTasks/DescribeClusters failure reason AWS returns for a resource
// that no longer exists (e.g. a task reaped between ListTasks and DescribeTasks). A MISSING resource is
// genuinely gone, so omitting it from the scan (and letting the prune soft-delete it) is correct; any
// other failure reason means the scan is incomplete and the prune must be skipped.
const ecsFailureReasonMissing = "MISSING"

// ECSTaskDetector discovers Fargate-cluster tasks: for any cluster running at least one Fargate task it
// stores EVERY task (regardless of launch type), since such a cluster is handled fully as fargate (the
// per-task sidecar). Pure-EC2 clusters store no tasks here — they're covered by the cluster agent in the
// compute-platform domain. This is the workload-domain counterpart to the compute-platform ECS cluster detector.
type ECSTaskDetector struct {
	store *store.Store
	cache *awsclient.Cache
	// tags returns the current discovery tag selectors (DB-backed live config): when non-empty, only tasks
	// carrying ALL of these tag key/values are discovered. A getter so a config change applies without a
	// restart.
	tags func() map[string]string
}

// NewECSTaskDetector builds the Fargate-task detector over the workload store and shared config cache.
// tags is a getter for the live discovery tag filter (nil/empty discovers all tasks).
func NewECSTaskDetector(st *store.Store, cache *awsclient.Cache, tags func() map[string]string) *ECSTaskDetector {
	return &ECSTaskDetector{store: st, cache: cache, tags: tags}
}

func (d *ECSTaskDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	regions, err := d.cache.EnabledRegions(ctx)
	if err != nil {
		return baseapi.DetectResult{}, fmt.Errorf("enumerate enabled regions: %w", err)
	}
	tags := d.tags()
	log.Info("ecs task detector starting", "regions", len(regions), "tag_filter", len(tags) > 0)
	var anyErr error
	authed := false
	for _, region := range regions {
		scanStart := time.Now()
		cfg, err := d.cache.Config(ctx, region)
		if err != nil {
			anyErr = err
			continue
		}
		tasks, err := discoverFargateClusterTasks(ctx, cfg, region, tags)
		if err != nil {
			anyErr = err
			log.Error("ecs task region scan failed", "region", region, "error", err)
			continue
		}
		authed = true
		upsertFailed := false
		for _, t := range tasks {
			if err := d.store.UpsertECSTask(ctx, t); err != nil {
				log.Error("upsert ecs task failed", "region", region, "arn", t.TaskARN, "error", err)
				upsertFailed = true
			}
		}
		_ = d.store.UpdateDiscoveryState(ctx, region, store.KindECSTask, scanStart)
		// Prune only when every discovered task persisted this cycle; pruning after a failed upsert would
		// soft-delete a still-live task whose last_seen_at wasn't refreshed this scan.
		if upsertFailed {
			anyErr = fmt.Errorf("region %s: one or more ecs task upserts failed; skipping prune", region)
			log.Error("skipping ecs task prune after upsert failure", "region", region)
		} else if err := d.store.PruneECSTasks(ctx, region, scanStart); err != nil {
			log.Error("prune ecs task inventory failed", "region", region, "error", err)
		}
		log.Info("ecs task region scan complete", "region", region, "tasks", len(tasks))
	}
	// Reclaim rows soft-deleted long ago (once per cycle, account-wide); ECS tasks churn fast, so this
	// bounds table growth. Best-effort — a purge failure never fails discovery.
	if err := d.store.PurgeDeletedECSTasks(ctx, time.Now().Add(-store.SoftDeleteRetention)); err != nil {
		log.Error("purge deleted ecs task inventory failed", "error", err)
	}
	return baseapi.DetectResult{Authenticated: authed}, anyErr
}

// discoverFargateClusterTasks enumerates clusters and returns the tasks of every cluster that runs at
// least one Fargate task (all of that cluster's tasks, regardless of launch type).
func discoverFargateClusterTasks(ctx context.Context, cfg aws.Config, region string, tags map[string]string) ([]store.ECSTask, error) {
	log.Info("ecs task discovery starting", "region", region)
	client := ecs.NewFromConfig(cfg)
	var out []store.ECSTask
	clusterPaginator := ecs.NewListClustersPaginator(client, &ecs.ListClustersInput{})
	for pageNum := 1; clusterPaginator.HasMorePages(); pageNum++ {
		page, err := clusterPaginator.NextPage(ctx)
		if err != nil {
			log.Error("ecs list clusters failed", "region", region, "page", pageNum, "error", err)
			return nil, err
		}
		for i := range page.ClusterArns {
			tasks, err := clusterTasksIfFargate(ctx, client, region, page.ClusterArns[i], tags)
			if err != nil {
				return nil, err
			}
			out = append(out, tasks...)
		}
	}
	log.Info("ecs task discovery complete", "region", region, "tasks", len(out))
	return out, nil
}

// clusterTasksIfFargate returns a cluster's tasks IFF it runs at least one Fargate task (then ALL its
// tasks); otherwise nil (pure-EC2 cluster — the cluster agent covers it). When tags is non-empty, only
// tasks carrying all of the given tag key/values are kept — DescribeTasks returns tags inline (Include
// TAGS), so no extra API call is needed.
func clusterTasksIfFargate(ctx context.Context, client *ecs.Client, region, clusterARN string, tags map[string]string) ([]store.ECSTask, error) {
	var all []store.ECSTask
	hasFargate := false
	cluster := clusterName(clusterARN)
	describeInput := func(batch []string) *ecs.DescribeTasksInput {
		in := &ecs.DescribeTasksInput{Cluster: aws.String(clusterARN), Tasks: batch}
		if len(tags) > 0 {
			in.Include = []ecstypes.TaskField{ecstypes.TaskFieldTags}
		}
		return in
	}
	paginator := ecs.NewListTasksPaginator(client, &ecs.ListTasksInput{Cluster: aws.String(clusterARN)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			log.Error("ecs list tasks failed", "region", region, "cluster", cluster, "error", err)
			return nil, err
		}
		for _, batch := range chunk(page.TaskArns, describeTasksBatch) {
			desc, err := client.DescribeTasks(ctx, describeInput(batch))
			if err != nil {
				log.Error("ecs describe tasks failed", "region", region, "cluster", cluster, "error", err)
				return nil, err
			}
			// A non-MISSING failure means this batch is incomplete (throttling, server error): return an error
			// so the region's prune is skipped rather than soft-deleting live tasks we just failed to describe.
			for _, f := range desc.Failures {
				if !strings.EqualFold(aws.ToString(f.Reason), ecsFailureReasonMissing) {
					return nil, fmt.Errorf("describe tasks incomplete in cluster %s: arn=%s reason=%s detail=%s",
						cluster, aws.ToString(f.Arn), aws.ToString(f.Reason), aws.ToString(f.Detail))
				}
			}
			for _, t := range desc.Tasks {
				if strings.EqualFold(string(t.LaunchType), store.LaunchTypeFargate) {
					hasFargate = true
				}
				if len(tags) > 0 && !ecsTagsMatchAll(t.Tags, tags) {
					continue // doesn't carry the required discovery tags
				}
				all = append(all, store.ECSTask{
					Region:         region,
					ClusterARN:     clusterARN,
					TaskARN:        aws.ToString(t.TaskArn),
					ServiceName:    serviceNameFromGroup(aws.ToString(t.Group)),
					TaskDefinition: aws.ToString(t.TaskDefinitionArn),
					LaunchType:     string(t.LaunchType),
					LastStatus:     aws.ToString(t.LastStatus),
					DesiredStatus:  aws.ToString(t.DesiredStatus),
					Metadata: map[string]any{
						"group":     aws.ToString(t.Group),
						"cpu":       aws.ToString(t.Cpu),
						"memory":    aws.ToString(t.Memory),
						"startedBy": aws.ToString(t.StartedBy),
					},
				})
			}
		}
	}
	if !hasFargate {
		return nil, nil
	}
	return all, nil
}

func clusterName(clusterARN string) string {
	if i := strings.LastIndex(clusterARN, "/"); i >= 0 {
		return clusterARN[i+1:]
	}
	return clusterARN
}

func serviceNameFromGroup(group string) string {
	const prefix = "service:"
	if strings.HasPrefix(group, prefix) {
		return strings.TrimPrefix(group, prefix)
	}
	return ""
}

// ecsTagsMatchAll reports whether the task's tags contain every key/value in required (reuses the shared
// tagsMatchAll from the Lambda detector).
func ecsTagsMatchAll(taskTags []ecstypes.Tag, required map[string]string) bool {
	have := make(map[string]string, len(taskTags))
	for _, t := range taskTags {
		have[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return tagsMatchAll(have, required)
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
