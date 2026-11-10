// Package agentcore owns the AgentCore runtime boundary shared by discovery and instrumentation.
package agentcore

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
)

const (
	Kind = "aws.bedrock-agentcore-runtime"
	// PackageEnv is the preparation contract written by the artifact build/deployment owner.
	// It declares the dormant bootstrap and the profiles included in the deployed artifact.
	PackageEnv = "ODIGOS_AGENTCORE_PACKAGE"
	// EntrypointEnv names a direct-code agent's own script when its entry point is the Odigos start wrapper alone.
	// AgentCore accepts a second entry point element only after its own opentelemetry-instrument prefix.
	EntrypointEnv = "ODIGOS_AGENTCORE_ENTRYPOINT"
	EnabledEnv    = "ODIGOS_AGENTCORE_ENABLED"
	DistroEnv     = "ODIGOS_AGENTCORE_DISTRO"
)

type Package struct {
	Version      int      `json:"v"`
	Language     string   `json:"language"`
	Distros      []string `json:"distros"`
	ModuleFormat string   `json:"moduleFormat,omitempty"`
	// Mode is how the SDK was delivered. Automatic instrumentation always adopts the SDK the artifact carries and
	// writes "adopt". Contracts that predate SDK below use it to tell the SDK apart: "adopt" for ADOT, absent for
	// the Odigos package; manual-mode preparation still writes them that way.
	Mode string `json:"mode,omitempty"`
	// SDK is which carried SDK adoption starts: "odigos" (the Odigos package or a prepared carrier bundle) or
	// "adot". Absent on contracts written before it was recorded; Mode then tells them apart.
	SDK string `json:"sdk,omitempty"`
}

func ParsePackage(value string) Package {
	var p Package
	if json.Unmarshal([]byte(value), &p) != nil || p.Version != 1 {
		return Package{}
	}
	return p
}

type Endpoint struct {
	Name          string `json:"name"`
	LiveVersion   string `json:"liveVersion"`
	TargetVersion string `json:"targetVersion"`
	Status        string `json:"status"`
}

// Runtime is deliberately an allowlist: raw environment, role credentials and exporter headers
// never enter general inventory. Rollback values live in the separate private snapshot table.
type Runtime struct {
	ARN            string     `json:"arn"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Region         string     `json:"region"`
	Version        string     `json:"version"`
	Status         string     `json:"status"`
	Language       string     `json:"language"`
	RuntimeVersion string     `json:"runtimeVersion"`
	ArtifactMode   string     `json:"artifactMode"`
	Image          string     `json:"image,omitempty"`
	NetworkMode    string     `json:"networkMode"`
	Instances      bool       `json:"instances"`
	Protocol       string     `json:"protocol"`
	Package        Package    `json:"package"`
	Endpoints      []Endpoint `json:"endpoints"`
	Origin         string     `json:"origin"`
	Distro         string     `json:"distro"`
	LastSeen       time.Time  `json:"-"`
}

func Describe(region string, live *ac.GetAgentRuntimeOutput, endpoints []Endpoint) Runtime {
	r := Runtime{ARN: aws.ToString(live.AgentRuntimeArn), ID: aws.ToString(live.AgentRuntimeId),
		Name: aws.ToString(live.AgentRuntimeName), Region: region, Version: aws.ToString(live.AgentRuntimeVersion),
		Status: string(live.Status), Package: ParsePackage(live.EnvironmentVariables[PackageEnv]),
		Endpoints: endpoints, Instances: live.CapacityProviderConfiguration != nil, Distro: live.EnvironmentVariables[DistroEnv]}
	r.Language = r.Package.Language
	if live.EnvironmentVariables["ODIGOS_MANAGED"] != "" {
		r.Origin = "connector"
	} else if live.EnvironmentVariables["ODIGOS_IAC"] != "" {
		r.Origin = "external"
	}
	switch a := live.AgentRuntimeArtifact.(type) {
	case *actypes.AgentRuntimeArtifactMemberCodeConfiguration:
		r.ArtifactMode = "code"
		runtime := string(a.Value.Runtime)
		if strings.HasPrefix(runtime, "PYTHON_") {
			r.Language = "python"
			r.RuntimeVersion = strings.ReplaceAll(strings.TrimPrefix(runtime, "PYTHON_"), "_", ".")
		}
		if strings.HasPrefix(runtime, "NODE_") {
			r.Language = "nodejs"
			r.RuntimeVersion = strings.TrimPrefix(runtime, "NODE_")
		}
	case *actypes.AgentRuntimeArtifactMemberContainerConfiguration:
		r.ArtifactMode = "container"
		r.Image = aws.ToString(a.Value.ContainerUri)
	}
	if live.NetworkConfiguration != nil {
		r.NetworkMode = string(live.NetworkConfiguration.NetworkMode)
	}
	if live.ProtocolConfiguration != nil {
		r.Protocol = string(live.ProtocolConfiguration.ServerProtocol)
	}
	return r
}

type Reader interface {
	ListAgentRuntimes(context.Context, *ac.ListAgentRuntimesInput, ...func(*ac.Options)) (*ac.ListAgentRuntimesOutput, error)
	GetAgentRuntime(context.Context, *ac.GetAgentRuntimeInput, ...func(*ac.Options)) (*ac.GetAgentRuntimeOutput, error)
	ListTagsForResource(context.Context, *ac.ListTagsForResourceInput, ...func(*ac.Options)) (*ac.ListTagsForResourceOutput, error)
	ListAgentRuntimeEndpoints(context.Context, *ac.ListAgentRuntimeEndpointsInput, ...func(*ac.Options)) (*ac.ListAgentRuntimeEndpointsOutput, error)
	GetAgentRuntimeEndpoint(context.Context, *ac.GetAgentRuntimeEndpointInput, ...func(*ac.Options)) (*ac.GetAgentRuntimeEndpointOutput, error)
}
type Client interface {
	Reader
	UpdateAgentRuntime(context.Context, *ac.UpdateAgentRuntimeInput, ...func(*ac.Options)) (*ac.UpdateAgentRuntimeOutput, error)
}
type Factory func(context.Context, string) (Client, error)

func NewFactory(config awsclient.ConfigSource) Factory {
	return func(ctx context.Context, region string) (Client, error) {
		cfg, err := config.Config(ctx, region)
		if err != nil {
			return nil, err
		}
		return ac.NewFromConfig(cfg), nil
	}
}

// Endpoints reads full endpoint facts: targetVersion alone does not prove traffic has cut over.
func Endpoints(ctx context.Context, client Reader, id string) ([]Endpoint, error) {
	var result []Endpoint
	var token *string
	for {
		page, err := client.ListAgentRuntimeEndpoints(ctx, &ac.ListAgentRuntimeEndpointsInput{AgentRuntimeId: aws.String(id), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, e := range page.RuntimeEndpoints {
			live, err := client.GetAgentRuntimeEndpoint(ctx, &ac.GetAgentRuntimeEndpointInput{AgentRuntimeId: aws.String(id), EndpointName: e.Name})
			if err != nil {
				return nil, err
			}
			result = append(result, Endpoint{Name: aws.ToString(live.Name), LiveVersion: aws.ToString(live.LiveVersion), TargetVersion: aws.ToString(live.TargetVersion), Status: string(live.Status)})
		}
		if aws.ToString(page.NextToken) == "" {
			return result, nil
		}
		token = page.NextToken
	}
}
