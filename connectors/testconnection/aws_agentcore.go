package testconnection

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/odigos-io/odigos-enterprise/connectors/awsregions"
)

func probeAgentCoreRegions(ctx context.Context, base aws.Config, regions []string) []string {
	failures := make([]string, len(regions))
	jobs := make(chan int, len(regions))
	for i, region := range regions {
		if awsregions.AgentCoreRuntimeSupported(region) {
			jobs <- i
		}
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(6, len(regions)) {
		workers.Go(func() {
			for i := range jobs {
				cfg := base
				cfg.Region = regions[i]
				probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err := probeAgentCore(probeCtx, ac.NewFromConfig(cfg))
				cancel()
				if err != nil {
					failures[i] = fmt.Sprintf("%s: %s", regions[i], summarizeErr(err))
				}
			}
		})
	}
	workers.Wait()
	var denied []string
	for _, failure := range failures {
		if failure != "" {
			denied = append(denied, failure)
		}
	}
	return denied
}

type agentCoreProbeClient interface {
	ListAgentRuntimes(context.Context, *ac.ListAgentRuntimesInput, ...func(*ac.Options)) (*ac.ListAgentRuntimesOutput, error)
	GetAgentRuntime(context.Context, *ac.GetAgentRuntimeInput, ...func(*ac.Options)) (*ac.GetAgentRuntimeOutput, error)
	ListTagsForResource(context.Context, *ac.ListTagsForResourceInput, ...func(*ac.Options)) (*ac.ListTagsForResourceOutput, error)
	ListAgentRuntimeEndpoints(context.Context, *ac.ListAgentRuntimeEndpointsInput, ...func(*ac.Options)) (*ac.ListAgentRuntimeEndpointsOutput, error)
	GetAgentRuntimeEndpoint(context.Context, *ac.GetAgentRuntimeEndpointInput, ...func(*ac.Options)) (*ac.GetAgentRuntimeEndpointOutput, error)
}

func probeAgentCore(ctx context.Context, client agentCoreProbeClient) error {
	page, err := client.ListAgentRuntimes(ctx, &ac.ListAgentRuntimesInput{MaxResults: aws.Int32(1)})
	if err != nil {
		return fmt.Errorf("bedrock-agentcore:ListAgentRuntimes: %w", err)
	}
	if len(page.AgentRuntimes) == 0 {
		return nil
	}
	runtime := page.AgentRuntimes[0]
	if _, err := client.GetAgentRuntime(ctx, &ac.GetAgentRuntimeInput{AgentRuntimeId: runtime.AgentRuntimeId}); err != nil {
		return fmt.Errorf("bedrock-agentcore:GetAgentRuntime: %w", err)
	}
	if _, err := client.ListTagsForResource(ctx, &ac.ListTagsForResourceInput{ResourceArn: runtime.AgentRuntimeArn}); err != nil {
		return fmt.Errorf("bedrock-agentcore:ListTagsForResource: %w", err)
	}
	endpoints, err := client.ListAgentRuntimeEndpoints(ctx, &ac.ListAgentRuntimeEndpointsInput{AgentRuntimeId: runtime.AgentRuntimeId, MaxResults: aws.Int32(1)})
	if err != nil {
		return fmt.Errorf("bedrock-agentcore:ListAgentRuntimeEndpoints: %w", err)
	}
	if len(endpoints.RuntimeEndpoints) > 0 {
		_, err = client.GetAgentRuntimeEndpoint(ctx, &ac.GetAgentRuntimeEndpointInput{AgentRuntimeId: runtime.AgentRuntimeId, EndpointName: endpoints.RuntimeEndpoints[0].Name})
		if err != nil {
			return fmt.Errorf("bedrock-agentcore:GetAgentRuntimeEndpoint: %w", err)
		}
	}
	return nil
}
