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

const (
	describeTasksBatch = 100
	// describeServicesBatch is the DescribeServices API maximum (10 services per call).
	describeServicesBatch = 10
)

// ecsFailureReasonMissing is the DescribeTasks/DescribeServices failure reason AWS returns for a resource
// that no longer exists (e.g. a task reaped between ListTasks and DescribeTasks). A MISSING resource is
// genuinely gone, so omitting it from the scan (and letting the prune soft-delete it) is correct; any
// other failure reason means the scan is incomplete and the prune must be skipped.
const ecsFailureReasonMissing = "MISSING"

// Fargate capacity provider names (AWS-reserved; customer capacity providers back EC2 Auto Scaling
// groups and always have different names).
const (
	capacityProviderFargate     = "FARGATE"
	capacityProviderFargateSpot = "FARGATE_SPOT"
)

// ECSTaskDetector discovers Fargate workloads in ONE region scan per cycle:
//
//   - ECS SERVICES running on Fargate (launch type or FARGATE/FARGATE_SPOT capacity providers) — the
//     per-service instrumentation unit, stored in ecs_services (stable across task churn).
//   - Fargate-launch-type TASKS — coverage detail for service tasks, and the workload identity for
//     standalone tasks (RunTask/EventBridge, no service).
//
// EC2-launch-type services and tasks are NOT stored: EC2-backed ECS is covered by the cluster agent
// (compute-platform domain), Fargate by the per-task sidecar this inventory backs.
type ECSTaskDetector struct {
	store *store.Store
	cache *awsclient.Cache
	// tags returns the current discovery tag selectors (DB-backed live config): when non-empty, only
	// services/tasks carrying ALL of these tag key/values are discovered. A getter so a config change
	// applies without a restart.
	tags func() map[string]string
}

// NewECSTaskDetector builds the Fargate detector over the workload store and shared config cache.
// tags is a getter for the live discovery tag filter (nil/empty discovers all services/tasks).
func NewECSTaskDetector(st *store.Store, cache *awsclient.Cache, tags func() map[string]string) *ECSTaskDetector {
	return &ECSTaskDetector{store: st, cache: cache, tags: tags}
}

func (d *ECSTaskDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	regions, err := d.cache.EnabledRegions(ctx)
	if err != nil {
		return baseapi.DetectResult{}, fmt.Errorf("enumerate enabled regions: %w", err)
	}
	tags := d.tags()
	log.Info("ecs fargate detector starting", "regions", len(regions), "tag_filter", len(tags) > 0)
	var anyErr error
	authed := false
	for _, region := range regions {
		scanStart := time.Now()
		cfg, err := d.cache.Config(ctx, region)
		if err != nil {
			anyErr = err
			continue
		}
		services, tasks, err := discoverFargateWorkloads(ctx, cfg, region, tags)
		if err != nil {
			anyErr = err
			log.Error("ecs fargate region scan failed", "region", region, "error", err)
			continue
		}
		authed = true
		upsertFailed := false
		for _, svc := range services {
			if err := d.store.UpsertECSService(ctx, svc); err != nil {
				log.Error("upsert ecs service failed", "region", region, "cluster", svc.ClusterARN, "service", svc.ServiceName, "error", err)
				upsertFailed = true
			}
		}
		for _, t := range tasks {
			if err := d.store.UpsertECSTask(ctx, t); err != nil {
				log.Error("upsert ecs task failed", "region", region, "arn", t.TaskARN, "error", err)
				upsertFailed = true
			}
		}
		_ = d.store.UpdateDiscoveryState(ctx, region, store.KindECSTask, scanStart)
		// Prune only when every discovered row persisted this cycle; pruning after a failed upsert would
		// soft-delete a still-live workload whose last_seen_at wasn't refreshed this scan.
		if upsertFailed {
			anyErr = fmt.Errorf("region %s: one or more ecs upserts failed; skipping prune", region)
			log.Error("skipping ecs prune after upsert failure", "region", region)
		} else {
			if err := d.store.PruneECSServices(ctx, region, scanStart); err != nil {
				log.Error("prune ecs service inventory failed", "region", region, "error", err)
			}
			if err := d.store.PruneECSTasks(ctx, region, scanStart); err != nil {
				log.Error("prune ecs task inventory failed", "region", region, "error", err)
			}
		}
		log.Info("ecs fargate region scan complete", "region", region, "services", len(services), "tasks", len(tasks))
	}
	// Reclaim rows soft-deleted long ago (once per cycle, account-wide); ECS tasks churn fast, so this
	// bounds table growth. Best-effort — a purge failure never fails discovery.
	if err := d.store.PurgeDeletedECSTasks(ctx, time.Now().Add(-store.SoftDeleteRetention)); err != nil {
		log.Error("purge deleted ecs task inventory failed", "error", err)
	}
	if err := d.store.PurgeDeletedECSServices(ctx, time.Now().Add(-store.SoftDeleteRetention)); err != nil {
		log.Error("purge deleted ecs service inventory failed", "error", err)
	}
	return baseapi.DetectResult{Authenticated: authed}, anyErr
}

// discoverFargateWorkloads enumerates the region's clusters and returns their Fargate services and
// Fargate tasks (one combined scan so services and tasks stay consistent within a cycle).
func discoverFargateWorkloads(ctx context.Context, cfg aws.Config, region string, tags map[string]string) ([]store.ECSService, []store.ECSTask, error) {
	log.Info("ecs fargate discovery starting", "region", region)
	client := ecs.NewFromConfig(cfg)
	var services []store.ECSService
	var tasks []store.ECSTask
	clusterPaginator := ecs.NewListClustersPaginator(client, &ecs.ListClustersInput{})
	for pageNum := 1; clusterPaginator.HasMorePages(); pageNum++ {
		page, err := clusterPaginator.NextPage(ctx)
		if err != nil {
			log.Error("ecs list clusters failed", "region", region, "page", pageNum, "error", err)
			return nil, nil, err
		}
		for i := range page.ClusterArns {
			clusterARN := page.ClusterArns[i]
			svcs, err := clusterFargateServices(ctx, client, region, clusterARN, tags)
			if err != nil {
				return nil, nil, err
			}
			services = append(services, svcs...)
			ts, err := clusterFargateTasks(ctx, client, region, clusterARN, tags)
			if err != nil {
				return nil, nil, err
			}
			tasks = append(tasks, ts...)
		}
	}
	log.Info("ecs fargate discovery complete", "region", region, "services", len(services), "tasks", len(tasks))
	return services, tasks, nil
}

// clusterFargateServices returns a cluster's Fargate ECS services (ListServices + DescribeServices in
// batches of 10). When tags is non-empty, only services carrying all of the given tag key/values are
// kept — DescribeServices returns tags inline (Include TAGS), so no extra API call is needed.
func clusterFargateServices(ctx context.Context, client *ecs.Client, region, clusterARN string, tags map[string]string) ([]store.ECSService, error) {
	var out []store.ECSService
	cluster := clusterName(clusterARN)
	paginator := ecs.NewListServicesPaginator(client, &ecs.ListServicesInput{Cluster: aws.String(clusterARN)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			log.Error("ecs list services failed", "region", region, "cluster", cluster, "error", err)
			return nil, err
		}
		for _, batch := range chunk(page.ServiceArns, describeServicesBatch) {
			in := &ecs.DescribeServicesInput{Cluster: aws.String(clusterARN), Services: batch}
			if len(tags) > 0 {
				in.Include = []ecstypes.ServiceField{ecstypes.ServiceFieldTags}
			}
			desc, err := client.DescribeServices(ctx, in)
			if err != nil {
				log.Error("ecs describe services failed", "region", region, "cluster", cluster, "error", err)
				return nil, err
			}
			// A non-MISSING failure means this batch is incomplete (throttling, server error): return an error
			// so the region's prune is skipped rather than soft-deleting live services we just failed to describe.
			for _, f := range desc.Failures {
				if !strings.EqualFold(aws.ToString(f.Reason), ecsFailureReasonMissing) {
					return nil, fmt.Errorf("describe services incomplete in cluster %s: arn=%s reason=%s detail=%s",
						cluster, aws.ToString(f.Arn), aws.ToString(f.Reason), aws.ToString(f.Detail))
				}
			}
			for _, svc := range desc.Services {
				if !isFargateService(svc) {
					continue // EC2/EXTERNAL service — the cluster agent's job, not the sidecar's
				}
				if len(tags) > 0 && !ecsTagsMatchAll(svc.Tags, tags) {
					continue // doesn't carry the required discovery tags
				}
				out = append(out, store.ECSService{
					Region:               region,
					ClusterARN:           clusterARN,
					ServiceName:          aws.ToString(svc.ServiceName),
					ServiceARN:           aws.ToString(svc.ServiceArn),
					TaskDefinition:       aws.ToString(svc.TaskDefinition),
					LaunchType:           string(svc.LaunchType),
					DeploymentController: deploymentControllerType(svc.DeploymentController),
					Status:               aws.ToString(svc.Status),
					DesiredCount:         svc.DesiredCount,
					RunningCount:         svc.RunningCount,
					// The awsvpc network configuration DescribeServices already returned and this
					// detector was discarding. It is what tells managed-Collector placement which VPC
					// this service runs on: awsvpcConfiguration names subnets and never the VPC, so the
					// subnet is resolved to its network later.
					SubnetIDs:        awsvpcSubnets(svc.NetworkConfiguration),
					SecurityGroupIDs: awsvpcSecurityGroups(svc.NetworkConfiguration),
					Metadata: map[string]any{
						"platformVersion":    aws.ToString(svc.PlatformVersion),
						"platformFamily":     aws.ToString(svc.PlatformFamily),
						"schedulingStrategy": string(svc.SchedulingStrategy),
					},
				})
			}
		}
	}
	return out, nil
}

// clusterFargateTasks returns a cluster's FARGATE-launch-type tasks (per-task filter; EC2 tasks are never
// stored). When tags is non-empty, only tasks carrying all of the given tag key/values are kept —
// DescribeTasks returns tags inline (Include TAGS), so no extra API call is needed.
func clusterFargateTasks(ctx context.Context, client *ecs.Client, region, clusterARN string, tags map[string]string) ([]store.ECSTask, error) {
	var out []store.ECSTask
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
				if !isFargateTask(t) {
					continue // EC2-launch-type task — the cluster agent's job, not the sidecar's
				}
				if len(tags) > 0 && !ecsTagsMatchAll(t.Tags, tags) {
					continue // doesn't carry the required discovery tags
				}
				out = append(out, store.ECSTask{
					Region:         region,
					ClusterARN:     clusterARN,
					TaskARN:        aws.ToString(t.TaskArn),
					ServiceName:    serviceNameFromGroup(aws.ToString(t.Group)),
					TaskDefinition: aws.ToString(t.TaskDefinitionArn),
					LaunchType:     launchTypeLabel(string(t.LaunchType), aws.ToString(t.CapacityProviderName)),
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
	return out, nil
}

// awsvpcSubnets and awsvpcSecurityGroups read a service's awsvpc network configuration, nil-safe at
// every level. Every service this detector stores runs on Fargate and therefore on awsvpc, so a nil
// configuration means DescribeServices did not echo one back rather than that the service has none —
// which is why an absent value is empty and never an error: placement reports a Fargate service it
// cannot attribute to a network, and a discovery gap must not stop the rest of the scan.
func awsvpcSubnets(nc *ecstypes.NetworkConfiguration) []string {
	if nc == nil || nc.AwsvpcConfiguration == nil {
		return nil
	}
	return nc.AwsvpcConfiguration.Subnets
}

func awsvpcSecurityGroups(nc *ecstypes.NetworkConfiguration) []string {
	if nc == nil || nc.AwsvpcConfiguration == nil {
		return nil
	}
	return nc.AwsvpcConfiguration.SecurityGroups
}

// isFargateService reports whether an ECS service runs its tasks on Fargate. Capacity-provider services
// report an EMPTY launch type, so the FARGATE/FARGATE_SPOT providers in the strategy are checked too; the
// Fargate-only platform family covers a service inheriting the cluster's default strategy (which
// DescribeServices may not echo back).
func isFargateService(svc ecstypes.Service) bool {
	switch svc.LaunchType {
	case ecstypes.LaunchTypeFargate:
		return true
	case ecstypes.LaunchTypeEc2, ecstypes.LaunchTypeExternal:
		return false
	}
	for _, cp := range svc.CapacityProviderStrategy {
		if isFargateCapacityProvider(aws.ToString(cp.CapacityProvider)) {
			return true
		}
	}
	return aws.ToString(svc.PlatformFamily) != ""
}

// isFargateTask reports whether a task runs on Fargate. Capacity-provider tasks report an EMPTY launch
// type and carry the provider name instead; the Fargate-only platform family is the final fallback.
func isFargateTask(t ecstypes.Task) bool {
	switch t.LaunchType {
	case ecstypes.LaunchTypeFargate:
		return true
	case ecstypes.LaunchTypeEc2, ecstypes.LaunchTypeExternal:
		return false
	}
	if isFargateCapacityProvider(aws.ToString(t.CapacityProviderName)) {
		return true
	}
	return aws.ToString(t.PlatformFamily) != ""
}

func isFargateCapacityProvider(name string) bool {
	return strings.EqualFold(name, capacityProviderFargate) || strings.EqualFold(name, capacityProviderFargateSpot)
}

// launchTypeLabel normalizes the stored launch type: capacity-provider Fargate tasks have an empty
// LaunchType, so label them FARGATE for a consistent inventory value.
func launchTypeLabel(launchType, capacityProvider string) string {
	if launchType != "" {
		return launchType
	}
	if isFargateCapacityProvider(capacityProvider) {
		return store.LaunchTypeFargate
	}
	return launchType
}

// deploymentControllerType returns the service's deployment controller type; a nil controller means the
// default ECS rolling-update controller.
func deploymentControllerType(dc *ecstypes.DeploymentController) string {
	if dc == nil {
		return string(ecstypes.DeploymentControllerTypeEcs)
	}
	return string(dc.Type)
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

// ecsTagsMatchAll reports whether the resource's tags contain every key/value in required (reuses the
// shared tagsMatchAll from the Lambda detector).
func ecsTagsMatchAll(resourceTags []ecstypes.Tag, required map[string]string) bool {
	have := make(map[string]string, len(resourceTags))
	for _, t := range resourceTags {
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
