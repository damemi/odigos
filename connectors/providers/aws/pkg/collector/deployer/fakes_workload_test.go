package deployer

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"
)

// taskDefinitionNotFound is ECS's answer for a family it has never seen: a ClientException whose
// message names the family, with no typed not-found of its own.
type taskDefinitionNotFound struct{ family string }

func (e taskDefinitionNotFound) Error() string {
	return "ClientException: Unable to describe task definition: " + e.family
}
func (taskDefinitionNotFound) ErrorCode() string             { return "ClientException" }
func (e taskDefinitionNotFound) ErrorMessage() string        { return e.Error() }
func (taskDefinitionNotFound) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

type fakeECS struct {
	clusters map[string]*ecstypes.Cluster // by name
	// families holds each family's revisions in order, and tags per revision ARN.
	families map[string][]ecstypes.TaskDefinition
	tags     map[string]map[string]string

	calls []string

	registerErr  error
	nextRevision int32
}

func newFakeECS() *fakeECS {
	return &fakeECS{
		clusters: map[string]*ecstypes.Cluster{},
		families: map[string][]ecstypes.TaskDefinition{},
		tags:     map[string]map[string]string{},
	}
}

func (f *fakeECS) withCluster(name string, tags map[string]string, providers []string) *fakeECS {
	arn := "arn:aws:ecs:" + testRegion + ":" + testAccount + ":cluster/" + name
	f.clusters[name] = &ecstypes.Cluster{
		ClusterArn:        aws.String(arn),
		ClusterName:       aws.String(name),
		Status:            aws.String(clusterStatusActive),
		CapacityProviders: providers,
		Tags:              ecsTags(tags),
	}
	// Addressable by ARN too, which is what a customer's clusterArn setting carries.
	f.clusters[arn] = f.clusters[name]
	return f
}

func (f *fakeECS) DescribeClusters(_ context.Context, in *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	f.calls = append(f.calls, "DescribeClusters:"+fmt.Sprint(in.Clusters))
	var out ecs.DescribeClustersOutput
	for _, ref := range in.Clusters {
		c, ok := f.clusters[ref]
		if !ok {
			// A cluster ECS cannot find is a FAILURE, not an error — which is why the code treats an
			// empty result as absent rather than looking for an exception.
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(ref), Reason: aws.String("MISSING")})
			continue
		}
		described := *c
		if !containsClusterField(in.Include, ecstypes.ClusterFieldTags) {
			// Tags come back only when asked for. A fake that always returned them would hide a missing
			// Include, and the ownership check would look like it worked.
			described.Tags = nil
		}
		out.Clusters = append(out.Clusters, described)
	}
	return &out, nil
}

func containsClusterField(fields []ecstypes.ClusterField, want ecstypes.ClusterField) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

// DescribeTaskDefinition resolves a family to its latest ACTIVE revision, which is what ECS does when
// given a bare family name.
func (f *fakeECS) DescribeTaskDefinition(_ context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	ref := aws.ToString(in.TaskDefinition)
	f.calls = append(f.calls, "DescribeTaskDefinition:"+ref)
	revisions := f.families[ref]
	if len(revisions) == 0 {
		return nil, taskDefinitionNotFound{family: ref}
	}
	latest := revisions[len(revisions)-1]
	out := &ecs.DescribeTaskDefinitionOutput{TaskDefinition: &latest}
	// Tags live in a field of the OUTPUT, not on the definition, and only when asked for.
	if containsTaskDefinitionField(in.Include, ecstypes.TaskDefinitionFieldTags) {
		for k, v := range f.tags[aws.ToString(latest.TaskDefinitionArn)] {
			out.Tags = append(out.Tags, ecstypes.Tag{Key: aws.String(k), Value: aws.String(v)})
		}
	}
	return out, nil
}

func containsTaskDefinitionField(fields []ecstypes.TaskDefinitionField, want ecstypes.TaskDefinitionField) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

// RegisterTaskDefinition stores the definition and populates the fields ECS adds on its own.
//
// Those additions are the whole reason this fake is not an echo: revision, status, ARN, compatibilities,
// requiresAttributes and the registration stamps are all absent from the request and present in the
// response, and a material-change test that compared request against response would see them and
// register a new revision on every pass. The round-trip test only means something because they are here.
func (f *fakeECS) RegisterTaskDefinition(_ context.Context, in *ecs.RegisterTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	family := aws.ToString(in.Family)
	f.calls = append(f.calls, "RegisterTaskDefinition:"+family)
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	f.nextRevision++
	arn := fmt.Sprintf("arn:aws:ecs:%s:%s:task-definition/%s:%d", testRegion, testAccount, family, f.nextRevision)

	td := ecstypes.TaskDefinition{
		TaskDefinitionArn:       aws.String(arn),
		Family:                  in.Family,
		Revision:                f.nextRevision,
		Status:                  ecstypes.TaskDefinitionStatusActive,
		Cpu:                     in.Cpu,
		Memory:                  in.Memory,
		NetworkMode:             in.NetworkMode,
		RequiresCompatibilities: in.RequiresCompatibilities,
		ExecutionRoleArn:        in.ExecutionRoleArn,
		RuntimePlatform:         in.RuntimePlatform,
		ContainerDefinitions:    in.ContainerDefinitions,

		// Everything below is ECS's, never the caller's.
		Compatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityEc2, ecstypes.CompatibilityFargate},
		RequiresAttributes: []ecstypes.Attribute{
			{Name: aws.String("com.amazonaws.ecs.capability.logging-driver.awslogs")},
			{Name: aws.String("ecs.capability.execution-role-awslogs")},
			{Name: aws.String("ecs.capability.secrets.asm.environment-variables")},
			{Name: aws.String("ecs.capability.task-eni")},
		},
		RegisteredAt: aws.Time(time.Now()),
		RegisteredBy: aws.String("arn:aws:iam::" + testAccount + ":role/odigos-connector"),
		Volumes:      []ecstypes.Volume{},
	}
	f.families[family] = append(f.families[family], td)
	tags := map[string]string{}
	for _, t := range in.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	f.tags[arn] = tags
	return &ecs.RegisterTaskDefinitionOutput{TaskDefinition: &td, Tags: in.Tags}, nil
}

// revisionCount is how many revisions a family holds — the churn measure the acceptance criterion is
// about.
func (f *fakeECS) revisionCount(family string) int { return len(f.families[family]) }

func (f *fakeECS) latest(family string) *ecstypes.TaskDefinition {
	revisions := f.families[family]
	if len(revisions) == 0 {
		return nil
	}
	return &revisions[len(revisions)-1]
}
