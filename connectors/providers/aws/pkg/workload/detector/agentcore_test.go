package detector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/agentcore"
)

type agentCoreDiscoveryClient struct {
	agentcore.Client
	fail         string
	lists, reads int
}

func (c *agentCoreDiscoveryClient) ListAgentRuntimes(_ context.Context, in *ac.ListAgentRuntimesInput, _ ...func(*ac.Options)) (*ac.ListAgentRuntimesOutput, error) {
	c.lists++
	if c.fail == "list" && in.NextToken != nil {
		return nil, errors.New("list failed on second page")
	}
	if in.NextToken == nil {
		return &ac.ListAgentRuntimesOutput{AgentRuntimes: []actypes.AgentRuntime{{AgentRuntimeId: aws.String("skip"), AgentRuntimeArn: aws.String("skip")}}, NextToken: aws.String("page2")}, nil
	}
	return &ac.ListAgentRuntimesOutput{AgentRuntimes: []actypes.AgentRuntime{{AgentRuntimeId: aws.String("demo"), AgentRuntimeArn: aws.String("arn:aws:bedrock-agentcore:us-east-1:123:runtime/demo")}}}, nil
}
func (c *agentCoreDiscoveryClient) ListTagsForResource(_ context.Context, in *ac.ListTagsForResourceInput, _ ...func(*ac.Options)) (*ac.ListTagsForResourceOutput, error) {
	if c.fail == "tags" {
		return nil, errors.New("tag failure")
	}
	value := "demo"
	if aws.ToString(in.ResourceArn) == "skip" {
		value = "other"
	}
	return &ac.ListTagsForResourceOutput{Tags: map[string]string{"application": value}}, nil
}
func (c *agentCoreDiscoveryClient) GetAgentRuntime(_ context.Context, in *ac.GetAgentRuntimeInput, _ ...func(*ac.Options)) (*ac.GetAgentRuntimeOutput, error) {
	c.reads++
	if c.fail == "runtime" {
		return nil, errors.New("get failure")
	}
	if aws.ToString(in.AgentRuntimeId) != "demo" {
		return nil, errors.New("tag filter did not run before get")
	}
	return &ac.GetAgentRuntimeOutput{AgentRuntimeId: in.AgentRuntimeId, AgentRuntimeArn: aws.String("arn:aws:bedrock-agentcore:us-east-1:123:runtime/demo"), AgentRuntimeName: aws.String("demo"), AgentRuntimeVersion: aws.String("3"), Status: "READY",
		EnvironmentVariables: map[string]string{agentcore.PackageEnv: `{"v":1,"language":"python","distros":["python-agentcore-adot"]}`, "PRIVATE_KEY": "sensitive-app-data", "OTEL_EXPORTER_OTLP_HEADERS": "secret-header"},
		AgentRuntimeArtifact: &actypes.AgentRuntimeArtifactMemberCodeConfiguration{Value: actypes.CodeConfiguration{Runtime: "PYTHON_3_12"}}}, nil
}
func (c *agentCoreDiscoveryClient) ListAgentRuntimeEndpoints(_ context.Context, in *ac.ListAgentRuntimeEndpointsInput, _ ...func(*ac.Options)) (*ac.ListAgentRuntimeEndpointsOutput, error) {
	if c.fail == "endpoints" {
		return nil, errors.New("endpoint list failure")
	}
	if in.NextToken == nil {
		return &ac.ListAgentRuntimeEndpointsOutput{RuntimeEndpoints: []actypes.AgentRuntimeEndpoint{{Name: aws.String("DEFAULT")}}, NextToken: aws.String("endpoints2")}, nil
	}
	return &ac.ListAgentRuntimeEndpointsOutput{RuntimeEndpoints: []actypes.AgentRuntimeEndpoint{{Name: aws.String("canary")}}}, nil
}
func (c *agentCoreDiscoveryClient) GetAgentRuntimeEndpoint(_ context.Context, in *ac.GetAgentRuntimeEndpointInput, _ ...func(*ac.Options)) (*ac.GetAgentRuntimeEndpointOutput, error) {
	if c.fail == "endpoint" {
		return nil, errors.New("endpoint get failure")
	}
	return &ac.GetAgentRuntimeEndpointOutput{Name: in.EndpointName, LiveVersion: aws.String("2"), TargetVersion: aws.String("3"), Status: "UPDATING"}, nil
}

type agentCoreDiscoveryStore struct {
	fail      bool
	inventory []agentcore.Runtime
	pruned    bool
}

func (s *agentCoreDiscoveryStore) UpsertAgentCore(_ context.Context, r agentcore.Runtime) error {
	if s.fail {
		return errors.New("persistence failure")
	}
	s.inventory = append(s.inventory, r)
	return nil
}
func (s *agentCoreDiscoveryStore) PruneAgentCore(context.Context, string, time.Time) error {
	s.pruned = true
	return nil
}

func TestAgentCoreDiscoveryPaginatesFiltersAndRedacts(t *testing.T) {
	st, client := &agentCoreDiscoveryStore{}, &agentCoreDiscoveryClient{}
	d := &AgentCoreDetector{store: st, tags: func() map[string]string { return map[string]string{"application": "demo"} }}
	if err := d.scan(context.Background(), client, "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !st.pruned || len(st.inventory) != 1 || client.lists != 2 || client.reads != 1 {
		t.Fatalf("incomplete scan: %+v client=%+v", st, client)
	}
	r := st.inventory[0]
	if r.Language != "python" || r.RuntimeVersion != "3.12" || len(r.Endpoints) != 2 || r.Endpoints[0].LiveVersion != "2" || r.Endpoints[0].TargetVersion != "3" {
		t.Fatalf("lost runtime or endpoint facts: %+v", r)
	}
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"PRIVATE_KEY", "sensitive-app-data", "OTEL_EXPORTER_OTLP_HEADERS", "secret-header"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("inventory leaked %s", secret)
		}
	}
}

func TestAgentCoreDiscoveryNeverPrunesPartialScan(t *testing.T) {
	for _, failure := range []string{"list", "tags", "runtime", "endpoints", "endpoint", "store"} {
		t.Run(failure, func(t *testing.T) {
			st := &agentCoreDiscoveryStore{fail: failure == "store"}
			d := &AgentCoreDetector{store: st, tags: func() map[string]string { return map[string]string{"application": "demo"} }}
			if err := d.scan(context.Background(), &agentCoreDiscoveryClient{fail: failure}, "us-east-1", time.Now()); err == nil {
				t.Fatal("failure was swallowed")
			}
			if st.pruned {
				t.Fatal("partial scan pruned existing inventory")
			}
		})
	}
}

type agentCoreRegionList []string

func (r agentCoreRegionList) EnabledRegions(context.Context) ([]string, error) { return r, nil }

func TestAgentCoreDiscoverySkipsUnsupportedRegions(t *testing.T) {
	st := &agentCoreDiscoveryStore{}
	var scanned []string
	d := &AgentCoreDetector{store: st, regions: agentCoreRegionList{"af-south-1", "us-east-1"}, tags: func() map[string]string { return map[string]string{"application": "demo"} },
		client: func(_ context.Context, region string) (agentcore.Client, error) {
			scanned = append(scanned, region)
			return &agentCoreDiscoveryClient{}, nil
		}}
	result, err := d.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Authenticated || len(scanned) != 1 || scanned[0] != "us-east-1" {
		t.Fatalf("unsupported region was queried: %v %+v", scanned, result)
	}
}
