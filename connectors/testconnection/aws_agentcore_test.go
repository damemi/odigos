package testconnection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

type agentCoreProbeFake struct {
	fail  string
	calls []string
	empty bool
}

func (f *agentCoreProbeFake) step(name string) error {
	f.calls = append(f.calls, name)
	if f.fail == name {
		return errors.New("denied")
	}
	return nil
}
func (f *agentCoreProbeFake) ListAgentRuntimes(_ context.Context, in *ac.ListAgentRuntimesInput, _ ...func(*ac.Options)) (*ac.ListAgentRuntimesOutput, error) {
	if err := f.step("ListAgentRuntimes"); err != nil {
		return nil, err
	}
	if aws.ToInt32(in.MaxResults) != 1 {
		return nil, errors.New("probe must be bounded")
	}
	out := &ac.ListAgentRuntimesOutput{}
	if !f.empty {
		out.AgentRuntimes = []actypes.AgentRuntime{{AgentRuntimeId: aws.String("demo"), AgentRuntimeArn: aws.String("arn")}}
	}
	return out, nil
}
func (f *agentCoreProbeFake) GetAgentRuntime(context.Context, *ac.GetAgentRuntimeInput, ...func(*ac.Options)) (*ac.GetAgentRuntimeOutput, error) {
	return &ac.GetAgentRuntimeOutput{}, f.step("GetAgentRuntime")
}
func (f *agentCoreProbeFake) ListTagsForResource(context.Context, *ac.ListTagsForResourceInput, ...func(*ac.Options)) (*ac.ListTagsForResourceOutput, error) {
	return &ac.ListTagsForResourceOutput{}, f.step("ListTagsForResource")
}
func (f *agentCoreProbeFake) ListAgentRuntimeEndpoints(context.Context, *ac.ListAgentRuntimeEndpointsInput, ...func(*ac.Options)) (*ac.ListAgentRuntimeEndpointsOutput, error) {
	return &ac.ListAgentRuntimeEndpointsOutput{RuntimeEndpoints: []actypes.AgentRuntimeEndpoint{{Name: aws.String("DEFAULT")}}}, f.step("ListAgentRuntimeEndpoints")
}
func (f *agentCoreProbeFake) GetAgentRuntimeEndpoint(context.Context, *ac.GetAgentRuntimeEndpointInput, ...func(*ac.Options)) (*ac.GetAgentRuntimeEndpointOutput, error) {
	return &ac.GetAgentRuntimeEndpointOutput{}, f.step("GetAgentRuntimeEndpoint")
}

func TestAgentCoreProbeOnlyReadsAndIdentifiesDeniedAction(t *testing.T) {
	for _, action := range []string{"", "ListAgentRuntimes", "GetAgentRuntime", "ListTagsForResource", "ListAgentRuntimeEndpoints", "GetAgentRuntimeEndpoint"} {
		t.Run(action, func(t *testing.T) {
			f := &agentCoreProbeFake{fail: action}
			err := probeAgentCore(context.Background(), f)
			if action == "" {
				if err != nil || len(f.calls) != 5 {
					t.Fatalf("incomplete probe: %v %v", f.calls, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "bedrock-agentcore:"+action) {
				t.Fatalf("wrong denied action: %v", err)
			}
		})
	}
	f := &agentCoreProbeFake{empty: true}
	if err := probeAgentCore(context.Background(), f); err != nil || len(f.calls) != 1 {
		t.Fatalf("empty-account probe must only list: %v %v", f.calls, err)
	}
}
