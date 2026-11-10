package detector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/odigos-io/odigos/connectors/awsregions"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/awsclient"
)

type agentCoreInventory interface {
	UpsertAgentCore(context.Context, agentcore.Runtime) error
	PruneAgentCore(context.Context, string, time.Time) error
	Now(context.Context) (time.Time, error)
}
type agentCoreRegions interface {
	EnabledRegions(context.Context) ([]string, error)
}
type AgentCoreDetector struct {
	store   agentCoreInventory
	regions agentCoreRegions
	client  agentcore.Factory
	tags    func() map[string]string
}

func NewAgentCoreDetector(st agentCoreInventory, cache *awsclient.Cache, tags func() map[string]string) *AgentCoreDetector {
	return &AgentCoreDetector{store: st, regions: cache, client: agentcore.NewFactory(cache), tags: tags}
}
func (d *AgentCoreDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	regions, err := d.regions.EnabledRegions(ctx)
	if err != nil {
		return baseapi.DetectResult{}, err
	}
	var errs []error
	authenticated := false
	type result struct {
		region string
		err    error
	}
	results := make(chan result, len(regions))
	jobs := make(chan string, len(regions))
	for _, region := range regions {
		if awsregions.AgentCoreRuntimeSupported(region) {
			jobs <- region
		}
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(6, len(regions)) {
		workers.Go(func() {
			for region := range jobs {
				regionCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				// The prune cutoff comes from the database clock, the one last_seen_at is written with.
				start, err := d.store.Now(regionCtx)
				var client agentcore.Client
				if err == nil {
					client, err = d.client(regionCtx, region)
				}
				if err == nil {
					err = d.scan(regionCtx, client, region, start)
				}
				cancel()
				results <- result{region, err}
			}
		})
	}
	workers.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			errs = append(errs, fmt.Errorf("AgentCore discovery %s: %w", result.region, result.err))
		} else {
			authenticated = true
		}
	}
	return baseapi.DetectResult{Authenticated: authenticated}, errors.Join(errs...)
}
func (d *AgentCoreDetector) scan(ctx context.Context, client agentcore.Reader, region string, start time.Time) error {
	var token *string
	for {
		page, err := client.ListAgentRuntimes(ctx, &ac.ListAgentRuntimesInput{NextToken: token})
		if err != nil {
			return err
		}
		for _, summary := range page.AgentRuntimes {
			tags, err := client.ListTagsForResource(ctx, &ac.ListTagsForResourceInput{ResourceArn: summary.AgentRuntimeArn})
			if err != nil {
				return err
			}
			var required map[string]string
			if d.tags != nil {
				required = d.tags()
			}
			if !tagsMatchAll(tags.Tags, required) {
				continue
			}
			live, err := client.GetAgentRuntime(ctx, &ac.GetAgentRuntimeInput{AgentRuntimeId: summary.AgentRuntimeId})
			if err != nil {
				return err
			}
			endpoints, err := agentcore.Endpoints(ctx, client, aws.ToString(summary.AgentRuntimeId))
			if err != nil {
				return err
			}
			if err := d.store.UpsertAgentCore(ctx, agentcore.Describe(region, live, endpoints)); err != nil {
				return err
			}
		}
		if aws.ToString(page.NextToken) == "" {
			break
		}
		token = page.NextToken
	}
	// Any read or persistence error above returns before pruning a partially scanned region.
	return d.store.PruneAgentCore(ctx, region, start)
}
