package instrumentor

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/agentcore"
)

// agentCoreArtifactRef is a lossless, typed snapshot of the two supported artifact union members.
// SDK unions cannot be round-tripped through interface{} JSON without their discriminator.
type agentCoreArtifactRef struct {
	Image      string   `json:"image,omitempty"`
	Bucket     string   `json:"bucket,omitempty"`
	Key        string   `json:"key,omitempty"`
	Version    string   `json:"version,omitempty"`
	Runtime    string   `json:"runtime,omitempty"`
	EntryPoint []string `json:"entryPoint,omitempty"`
}

func agentCoreArtifact(a actypes.AgentRuntimeArtifact) (agentCoreArtifactRef, error) {
	switch a := a.(type) {
	case *actypes.AgentRuntimeArtifactMemberContainerConfiguration:
		return agentCoreArtifactRef{Image: aws.ToString(a.Value.ContainerUri)}, nil
	case *actypes.AgentRuntimeArtifactMemberCodeConfiguration:
		s3, ok := a.Value.Code.(*actypes.CodeMemberS3)
		if !ok {
			return agentCoreArtifactRef{}, fmt.Errorf("%w: AgentCore source must be an S3 ZIP", ErrUnsupportedInstrumentation)
		}
		return agentCoreArtifactRef{Bucket: aws.ToString(s3.Value.Bucket), Key: aws.ToString(s3.Value.Prefix), Version: aws.ToString(s3.Value.VersionId), Runtime: string(a.Value.Runtime), EntryPoint: append([]string(nil), a.Value.EntryPoint...)}, nil
	}
	return agentCoreArtifactRef{}, fmt.Errorf("%w: unknown AgentCore artifact", ErrUnsupportedInstrumentation)
}
func (a agentCoreArtifactRef) sdk() actypes.AgentRuntimeArtifact {
	if a.Image != "" {
		return &actypes.AgentRuntimeArtifactMemberContainerConfiguration{Value: actypes.ContainerConfiguration{ContainerUri: aws.String(a.Image)}}
	}
	var version *string
	if a.Version != "" {
		version = aws.String(a.Version)
	}
	return &actypes.AgentRuntimeArtifactMemberCodeConfiguration{Value: actypes.CodeConfiguration{Code: &actypes.CodeMemberS3{Value: actypes.S3Location{Bucket: aws.String(a.Bucket), Prefix: aws.String(a.Key), VersionId: version}}, Runtime: actypes.AgentManagedRuntimeType(a.Runtime), EntryPoint: append([]string(nil), a.EntryPoint...)}}
}
func (a agentCoreArtifactRef) equal(b agentCoreArtifactRef) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// agentCoreInstallation is what one runtime should run to start the SDK adoption chose.
type agentCoreInstallation struct {
	SDK agentCoreSDKKind
	// Artifact is what the runtime should reference: unchanged for images, the same code with
	// a new entry point for a ZIP that starts through a launcher or wrapper. Never a different artifact:
	// Odigos does not rewrite or republish the customer's code.
	Artifact  agentCoreArtifactRef
	Package   agentcore.Package // Mode and SDK set; Distros as the chosen SDK can start them
	LoaderEnv map[string]string
}
type agentCoreInstaller interface {
	// Resolve detects what the deployed artifact carries and prepares the delivery for it. A non-nil wait means
	// the artifact carries no SDK Odigos may configure; the installation is then empty.
	Resolve(context.Context, *ac.GetAgentRuntimeOutput, basestore.SourceRecord) (agentCoreInstallation, *agentCoreWaiting, error)
}

// Only regular script entry points are qualified. We preserve their argument vector and normal
// main-module semantics instead of importing business modules from a wrapper process.
func agentCoreScript(entry []string, language string) error {
	if len(entry) == 0 || strings.HasPrefix(entry[0], "-") || filepath.IsAbs(entry[0]) || strings.Contains(entry[0], "\\") {
		return fmt.Errorf("%w: AgentCore ZIP needs a relative script entry point", ErrUnsupportedInstrumentation)
	}
	clean := filepath.ToSlash(filepath.Clean(entry[0]))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: invalid script entry point", ErrUnsupportedInstrumentation)
	}
	ext := filepath.Ext(clean)
	if language == "python" && ext == ".py" || language == "nodejs" && (ext == ".js" || ext == ".cjs") {
		return nil
	}
	return fmt.Errorf("%w: automatic AgentCore installation requires a Python script or CommonJS Node script; native ESM is not qualified", ErrUnsupportedInstrumentation)
}
