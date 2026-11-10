package testconnection

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

// Share the already-authenticated config and credential cache across regions.
// Rebuilding it per region repeats AssumeRole and serializes onboarding behind
// every regional endpoint. Bound concurrency, preserve region order in errors,
// and let slow/unreachable endpoints fail as checks within the overall deadline.
func probeAWSRegions(ctx context.Context, base aws.Config, regions []string, lambdaActive, ecsActive, tasks, services bool) ([]string, []string) {
	type failure struct{ lambda, ecs string }
	failures := make([]failure, len(regions))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(6, len(regions)) {
		workers.Go(func() {
			for i := range jobs {
				regionCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				cfg := base
				cfg.Region = regions[i]
				if lambdaActive {
					client := lambda.NewFromConfig(cfg)
					page, err := client.ListFunctions(regionCtx, &lambda.ListFunctionsInput{MaxItems: aws.Int32(1)})
					if err == nil {
						err = probeLambdaResource(regionCtx, client, page.Functions)
					}
					if err != nil {
						failures[i].lambda = regions[i] + ": " + summarizeErr(err)
					}
				}
				if ecsActive {
					client := ecs.NewFromConfig(cfg)
					page, err := client.ListClusters(regionCtx, &ecs.ListClustersInput{MaxResults: aws.Int32(1)})
					if err == nil {
						err = probeECSResources(regionCtx, client, page.ClusterArns, tasks, services)
					}
					if err != nil {
						failures[i].ecs = regions[i] + ": " + summarizeErr(err)
					}
				}
				cancel()
			}
		})
	}
	for i := range regions {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	var lambdaDenied, ecsDenied []string
	for _, failure := range failures {
		if failure.lambda != "" {
			lambdaDenied = append(lambdaDenied, failure.lambda)
		}
		if failure.ecs != "" {
			ecsDenied = append(ecsDenied, failure.ecs)
		}
	}
	return lambdaDenied, ecsDenied
}
