package awsprovider

import (
	"context"
	"fmt"
	"sort"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	workloadstore "github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

// CollectorRoutingProgress reads applied workload routing from the AWS inventory. It is a
// distribution of all instrumented workloads in each region; a workload still on the direct
// route may be awaiting a batch or may not be eligible for the regional Collector.
func (p *Provider) CollectorRoutingProgress(ctx context.Context) (map[string]baseapi.CollectorRoutingProgress, error) {
	if p.workloadStdb == nil {
		return nil, fmt.Errorf("AWS workload store is unavailable")
	}
	services, err := p.workloadStdb.ListECSServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ECS services for Collector routing progress: %w", err)
	}
	functions, err := p.workloadStdb.ListLambdaFunctions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Lambda functions for Collector routing progress: %w", err)
	}
	return collectorRoutingProgress(services, functions), nil
}

func collectorRoutingProgress(services []workloadstore.ECSService, functions []workloadstore.LambdaFunction) map[string]baseapi.CollectorRoutingProgress {
	out := map[string]baseapi.CollectorRoutingProgress{}
	add := func(region, ref, via, failure string) {
		progress := out[region]
		progress.Instrumented++
		if via == string(domaindest.RouteCollector) {
			progress.Routed++
		}
		if failure != "" {
			progress.Failed++
			progress.FailureRefs = append(progress.FailureRefs, ref)
		}
		out[region] = progress
	}
	for _, service := range services {
		if service.Instrumented {
			add(service.Region, fargateRouteRef(service), service.RoutingVia, service.CutoverFailure.Reason)
		}
	}
	for _, function := range functions {
		if function.Instrumented {
			add(function.Region, function.FunctionARN, function.RoutingVia, function.CutoverFailure.Reason)
		}
	}
	for region, progress := range out {
		sort.Strings(progress.FailureRefs)
		if len(progress.FailureRefs) > 3 {
			progress.FailureRefs = progress.FailureRefs[:3]
		}
		out[region] = progress
	}
	return out
}
