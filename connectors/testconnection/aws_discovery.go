package testconnection

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

func probeLambdaResource(ctx context.Context, client *lambda.Client, functions []lambdatypes.FunctionConfiguration) error {
	if len(functions) == 0 {
		return nil
	}
	arn := functions[0].FunctionArn
	if _, err := client.ListTags(ctx, &lambda.ListTagsInput{Resource: arn}); err != nil {
		return fmt.Errorf("lambda:ListTags: %w", err)
	}
	if _, err := client.ListAliases(ctx, &lambda.ListAliasesInput{FunctionName: arn, MaxItems: aws.Int32(1)}); err != nil {
		return fmt.Errorf("lambda:ListAliases: %w", err)
	}
	if _, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: arn}); err != nil {
		return fmt.Errorf("lambda:GetFunctionConfiguration: %w", err)
	}
	return nil
}

func probeECSResources(ctx context.Context, client *ecs.Client, clusters []string, fargate, compute bool) error {
	if len(clusters) == 0 {
		return nil
	}
	cluster := aws.String(clusters[0])
	if compute {
		if _, err := client.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: clusters[:1]}); err != nil {
			return fmt.Errorf("ecs:DescribeClusters: %w", err)
		}
	}
	tasks, err := client.ListTasks(ctx, &ecs.ListTasksInput{Cluster: cluster, MaxResults: aws.Int32(1), LaunchType: ecstypes.LaunchTypeFargate})
	if err != nil {
		return fmt.Errorf("ecs:ListTasks: %w", err)
	}
	if !fargate {
		return nil
	}
	if len(tasks.TaskArns) > 0 {
		if _, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: cluster, Tasks: tasks.TaskArns, Include: []ecstypes.TaskField{ecstypes.TaskFieldTags}}); err != nil {
			return fmt.Errorf("ecs:DescribeTasks: %w", err)
		}
	}
	services, err := client.ListServices(ctx, &ecs.ListServicesInput{Cluster: cluster, MaxResults: aws.Int32(1)})
	if err != nil {
		return fmt.Errorf("ecs:ListServices: %w", err)
	}
	if len(services.ServiceArns) == 0 {
		return nil
	}
	live, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: cluster, Services: services.ServiceArns, Include: []ecstypes.ServiceField{ecstypes.ServiceFieldTags}})
	if err != nil {
		return fmt.Errorf("ecs:DescribeServices: %w", err)
	}
	if len(live.Failures) > 0 {
		return fmt.Errorf("ecs:DescribeServices returned a resource failure: %s", aws.ToString(live.Failures[0].Reason))
	}
	for _, service := range live.Services {
		if service.TaskDefinition != nil {
			if _, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: service.TaskDefinition}); err != nil {
				return fmt.Errorf("ecs:DescribeTaskDefinition: %w", err)
			}
		}
	}
	return nil
}
