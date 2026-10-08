package deployer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/opamp"
)

// The Supervisor entrypoint, exactly as collector/managed/README.md specifies it.
//
// The README writes it as `command: ["/odigosopampsupervisor"]` plus `args: ["--config=…"]`, which is
// Kubernetes vocabulary. ECS has no `args`: `entryPoint` is the image's ENTRYPOINT and `command` is its
// CMD. The image sets no ENTRYPOINT and `CMD ["/odigosotelcol"]`, so overriding `command` with both
// elements is the same override — the binary and its flag — and leaving `entryPoint` alone keeps the
// image's own contract intact.
const (
	supervisorBinary     = "/odigosopampsupervisor"
	supervisorConfigFlag = "--config=/etc/odigos/managed-collector/supervisor.yaml"
)

// Container environment the Supervisor's bootstrap config substitutes. See
// collector/managed/supervisor.yaml — every one of these appears there as ${…}, so an omission is a
// Supervisor that starts with an empty endpoint or an unidentifiable agent rather than one that fails
// loudly.
const (
	// envCloudConnectorID must be the CONNECTOR UID, not the connector name: the OpAMP server verifies
	// the agent's reported odigos.cloud.connector.id against the binding its credential was issued
	// under (base/opamp.AttrConnectorID), and a mismatch is a refused handshake.
	envCloudConnectorID = "ODIGOS_CLOUD_CONNECTOR_ID"
	envCloudProvider    = "ODIGOS_CLOUD_PROVIDER"
)

// awslogs driver options.
const (
	logDriverAWSLogs   = "awslogs"
	logOptionGroup     = "awslogs-group"
	logOptionRegion    = "awslogs-region"
	logOptionPrefix    = "awslogs-stream-prefix"
	logStreamPrefix    = "collector"
	operatingSystemFam = "LINUX"
)

// TaskDefinitionClient is the slice of ECS this file uses. DescribeTaskDefinition is declared under
// MANAGEMENT — a drift verdict in manual mode compares against the definition actually deployed — and
// RegisterTaskDefinition under DEPLOYMENT.
type TaskDefinitionClient interface {
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
}

// TaskDefinitionInput is what rendering and converging one regional Collector's task definition depends
// on. HostNetworkID is physical placement metadata; the Collector identity is the region on the caller's
// basestore.CollectorKey.
type TaskDefinitionInput struct {
	HostNetworkID string
	Region        string
	Connector     string
	ConnectorID   string
	Identity      collectorownership.Identity
	Mode          string

	// ExecutionRoleARN is the customer-owned role ECS uses to pull the image, read the two
	// secrets and write logs. The Collector container itself receives no task role because it
	// does not call AWS APIs.
	ExecutionRoleARN string
	LogGroup         string
	// Delivery carries the two secret refs. Refs only — the type has no field a value could live in.
	Delivery collectorimage.Delivery
	// OpAMPEndpoint is the PUBLIC endpoint the Collector dials back to, from config.
	OpAMPEndpoint string
	// Pin is the image this connector build deploys. Compiled in rather than configured, so a customer
	// applying a published artifact and a Collector Odigos deployed get identical bytes.
	Pin collectorimage.Pin
	// Arch, CPU, Memory and ImageRepositoryOverride come from `collector.regions.<region>`.
	Arch                    string
	CPU                     string
	Memory                  string
	ImageRepositoryOverride string
}

// TaskDefinition is the revision a service should run.
type TaskDefinition struct {
	ARN      string
	Family   string
	Revision int32
	// Registered is whether THIS pass registered a new revision. False is the steady state, and the
	// property that matters: see EnsureTaskDefinition.
	Registered bool
	// Image is the digest-pinned reference the revision runs, for reporting and for the drift check.
	Image string
	// Architecture is the ECS spelling of the CPU architecture the revision declares.
	Architecture string
	Problems     []collectorstatus.Problem
}

// Ready reports whether a service could be created from this revision.
func (t TaskDefinition) Ready() bool {
	return t.ARN != "" && !collectorstatus.HasError(t.Problems)
}

// TaskDefinitionSpec is exactly the set of fields Odigos decides about a task definition.
//
// It exists so "has anything material changed" is a comparison between two values of one type rather
// than between a request and an API response. A described definition is decoded back into this shape
// (specFromDefinition) and fingerprinted the same way, which is what keeps the answer honest: comparing
// a RegisterTaskDefinitionInput against a TaskDefinition directly would differ on every pass, because
// ECS populates a dozen fields nobody asked for — revision, status, compatibilities, requiresAttributes,
// registeredAt — and a comparison that saw those would register a new revision every reconcile. Every
// revision is a rolling replacement of the network's Collector, so that is a fleet that never settles.
type TaskDefinitionSpec struct {
	Family                  string
	CPU                     string
	Memory                  string
	Architecture            string
	OperatingSystemFamily   string
	NetworkMode             string
	RequiresCompatibilities []string
	ExecutionRoleARN        string

	// Containers is the whole container list, not just ours. A revision somebody added a sidecar to has
	// a different shape from the one Odigos renders, and noticing that is the point: a family Odigos owns
	// should hold Odigos's definition and nothing else.
	Containers []ContainerSpec
}

// ContainerSpec is one container's Odigos-decided fields.
type ContainerSpec struct {
	Name  string
	Image string
	// RepositoryCredentialsRef is the Secrets Manager JSON object ECS uses to authenticate before
	// starting the container. The task also selects its password field for ODIGOS_ONPREM_TOKEN.
	RepositoryCredentialsRef string
	Essential                bool
	Command                  []string
	Ports                    []int
	// Environment is plain values. collectorimage.EnvProblems refuses every name in SecretEnvVars here —
	// on ECS anyone holding ecs:DescribeTaskDefinition can read a plain value, which is a far wider set
	// of people than those who can read a secret.
	Environment map[string]string
	// Secrets maps an environment variable name to the secret ref it is populated from.
	Secrets map[string]string
	// LogDriver and LogOptions are the awslogs configuration.
	LogDriver  string
	LogOptions map[string]string
}

// Fingerprint is the material-change test: a digest over every field Odigos decides, and nothing else.
//
// A digest rather than a struct comparison because it is what gets logged and reported — two hex
// strings tell an operator that something changed without printing a task definition — and because a
// canonical rendering forces every map to be sorted, which is what stops a reordered environment from
// reading as a change.
func (s TaskDefinitionSpec) Fingerprint() string {
	var b strings.Builder
	write := func(parts ...string) {
		for _, p := range parts {
			b.WriteString(p)
			b.WriteByte(0)
		}
	}
	write("family", s.Family, "cpu", s.CPU, "memory", s.Memory,
		"arch", s.Architecture, "os", s.OperatingSystemFamily,
		"network", s.NetworkMode, "exec", s.ExecutionRoleARN)
	write("compat")
	for _, c := range sortedCopy(s.RequiresCompatibilities) {
		write(c)
	}
	for _, c := range s.Containers {
		write("container", c.Name, "image", c.Image, "repository-credentials", c.RepositoryCredentialsRef,
			"essential", strconv.FormatBool(c.Essential))
		write("command")
		for _, part := range c.Command {
			write(part)
		}
		write("ports")
		for _, p := range sortedInts(c.Ports) {
			write(strconv.Itoa(p))
		}
		write("env")
		for _, k := range sortedKeys(c.Environment) {
			write(k, c.Environment[k])
		}
		write("secrets")
		for _, k := range sortedKeys(c.Secrets) {
			write(k, c.Secrets[k])
		}
		write("logs", c.LogDriver)
		for _, k := range sortedKeys(c.LogOptions) {
			write(k, c.LogOptions[k])
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// RenderTaskDefinition builds the spec Odigos wants for one regional Collector, or the reasons it cannot.
//
// Pure, and every refusal here happens before any AWS call. That ordering is the point: an image that
// cannot be resolved or a cpu/memory pair Fargate does not offer would otherwise be discovered by
// RegisterTaskDefinition failing on every pass, with an error that names no field.
func RenderTaskDefinition(in TaskDefinitionInput) (TaskDefinitionSpec, []collectorstatus.Problem) {
	var problems []collectorstatus.Problem

	arch := collectorimage.Arch(in.Arch)
	if arch == "" {
		arch = collectorimage.DefaultArch
	}
	image, err := collectorimage.Resolve(imagePin(in), collectorimage.CloudAWS, arch, collectorimage.Override{})
	if err != nil {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemImageUnavailable,
			Severity: collectorstatus.SeverityError,
			Message:  fmt.Sprintf("no Collector is deployed on %s: %v", in.HostNetworkID, err),
		})
	}
	cpuArch, ok := ecsCPUArchitecture(arch)
	if !ok {
		// Resolve has already refused an architecture the pin does not carry; this catches one the pin
		// declares that ECS has no spelling for, which would otherwise register a definition with an
		// empty runtimePlatform and place tasks on whatever the default is.
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemImageUnavailable,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector is deployed on %s: %q is not an architecture ECS can run a "+
				"Fargate task on", in.HostNetworkID, arch),
		})
	}

	cpu, memory := in.CPU, in.Memory
	problems = append(problems, checkFargateSize(in.Region, cpu, memory)...)

	if in.OpAMPEndpoint == "" {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemImageUnavailable,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector is deployed on %s: %s is not set on this connector, so the "+
				"Supervisor would start with no OpAMP endpoint and the Collector would serve the inert "+
				"bootstrap configuration for ever. collectorgateway.CheckEndpoint reports this at the "+
				"capability level", in.HostNetworkID, opamp.EnvEndpoint),
		})
	}

	environment := map[string]string{
		opamp.EnvEndpoint:   in.OpAMPEndpoint,
		envCloudConnectorID: in.ConnectorID,
		envCloudProvider:    string(collectorimage.CloudAWS),
	}
	// The shared check names: the deployer against the definition it is about to register, the
	// artifact guard against every published variant. It refuses the two secret names as plain values.
	problems = append(problems, collectorimage.EnvProblems(environment)...)
	problems = append(problems, collectorimage.DeliveryProblems(in.Delivery)...)

	if collectorstatus.HasError(problems) {
		return TaskDefinitionSpec{}, problems
	}

	return TaskDefinitionSpec{
		Family:       TaskDefinitionFamily(in.Region),
		CPU:          cpu,
		Memory:       memory,
		Architecture: cpuArch,
		// LINUX explicitly. The Collector is a Linux binary and a Windows task definition would place
		// nowhere; ECS defaults this, and a default that changes is a default worth not relying on.
		OperatingSystemFamily: operatingSystemFam,
		// awsvpc, which is not a choice: a Fargate task has no other network mode, and it is what gives
		// the task its own ENI — the thing the security-group rules are written against.
		NetworkMode:             string(ecstypes.NetworkModeAwsvpc),
		RequiresCompatibilities: []string{string(ecstypes.CompatibilityFargate)},
		ExecutionRoleARN:        in.ExecutionRoleARN,
		Containers: []ContainerSpec{{
			Name:                     CollectorContainerName,
			Image:                    image.Ref,
			RepositoryCredentialsRef: in.Delivery.LicenseSecretRef,
			Essential:                true,
			Command:                  []string{supervisorBinary, supervisorConfigFlag},
			Ports:                    collectorPorts(),
			// No readonlyRootFilesystem and no volume: the Supervisor writes its instance identity and
			// cached config under /var/lib/odigos/collector-supervisor, and v1 runs that on the task's
			// own ephemeral storage. EFS is the later opt-in for queue durability.
			Environment: environment,
			Secrets:     collectorSecrets(in.Delivery),
			LogDriver:   logDriverAWSLogs,
			LogOptions: map[string]string{
				logOptionGroup:  in.LogGroup,
				logOptionRegion: in.Region,
				logOptionPrefix: logStreamPrefix,
			},
		}},
	}, problems
}

// ecsCPUArchitecture translates the provider-neutral image architecture into the spelling expected
// by ECS. The shared image contract intentionally has no knowledge of provider runtime enums.
func ecsCPUArchitecture(arch collectorimage.Arch) (string, bool) {
	switch arch {
	case collectorimage.ArchAMD64:
		return "X86_64", true
	case collectorimage.ArchARM64:
		return "ARM64", true
	default:
		return "", false
	}
}

// collectorSecrets maps the environment variables a Collector's secret material arrives in to the refs
// it comes from.
//
// The bootstrap document is included ONLY when a secret actually holds one, and that condition is the
// whole reason this is a function. ECS resolves every `secrets` entry when a task starts and fails the
// task if one cannot be read, so a reference to a secret with no version produces a
// ResourceInitializationError — a Collector that will not start at all, caused by the optional mitigation
// that exists to keep one running. The licence follows the same rule one level up: EnsureNetwork
// withholds its ref when the connector holds no token.
func collectorSecrets(delivery collectorimage.Delivery) map[string]string {
	secrets := map[string]string{
		collectorimage.EnvOpAMPAuthorization: delivery.CredentialSecretRef,
		// ECS receives the full JSON object as repository credentials; the Collector still receives
		// the original token from its password field.
		collectorimage.EnvOnPremToken: secretJSONKey(delivery.LicenseSecretRef, "password"),
	}
	if delivery.BootstrapSecretRef != "" {
		secrets[collectorimage.EnvBootstrapConfig] = delivery.BootstrapSecretRef
	}
	return secrets
}

func secretJSONKey(secretARN, key string) string {
	if secretARN == "" {
		return ""
	}
	return secretARN + ":" + key + "::"
}

// collectorPorts are the four ports lists. 13134 is included even though it is never exposed through
// the load balancer: the container listens on it, and a task definition that did not declare it would
// leave the Supervisor's own health endpoint undocumented in the one place an operator looks.
func collectorPorts() []int {
	return []int{
		collectoringress.PortOTLPGRPC,
		collectoringress.PortOTLPHTTP,
		collectoringress.PortHealth,
		collectoringress.PortSupervisorHealth,
	}
}

// imagePin applies a network's repository override to the compiled-in pin.
//
// The DIGEST is untouched, which is the whole contract: an override says where to pull the bytes from —
// an ECR pull-through cache, a private mirror — and the bytes still have to be the ones this connector
// build was released against. A copy, because the compiled-in pin is a package-level value and mutating
// its map would change every other network's image.
func imagePin(in TaskDefinitionInput) collectorimage.Pin {
	if in.ImageRepositoryOverride == "" {
		return in.Pin
	}
	pin := in.Pin
	pin.Repositories = map[collectorimage.Cloud]string{collectorimage.CloudAWS: in.ImageRepositoryOverride}
	return pin
}

// EnsureTaskDefinition converges one network's task definition family.
//
// It registers a revision only when the rendered definition differs from the family's ACTIVE one, and
// that restraint is the acceptance criterion because of what a revision costs downstream: the service
// rolls onto it, every task is replaced, and a Collector on ephemeral storage loses its queue when it
// restarts. A comparison that was wrong in the churning direction would replace every Collector
// in the fleet on every reconcile pass, for ever, and nothing in the resulting behaviour would look
// like a bug — just a Collector that is always mid-deployment.
func EnsureTaskDefinition(ctx context.Context, api TaskDefinitionClient, in TaskDefinitionInput) (TaskDefinition, error) {
	out := TaskDefinition{Family: TaskDefinitionFamily(in.Region)}
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		out.Problems = problems
		return out, nil
	}

	desired, problems := RenderTaskDefinition(in)
	out.Problems = problems
	if collectorstatus.HasError(problems) {
		return out, nil
	}

	active, activeTags, err := describeActiveTaskDefinition(ctx, api, out.Family)
	if err != nil {
		return out, err
	}
	if active != nil {
		object := collectorownership.Object{
			Ref:  aws.ToString(active.TaskDefinitionArn),
			Tags: baseapi.ParseCollectorTags(ecsTagMap(activeTags)),
		}
		if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
			out.Problems = append(out.Problems, notOurs("task definition family", out.Family, object, in.Identity))
			return out, nil
		}
		if specFromDefinition(active).Fingerprint() == desired.Fingerprint() {
			out.ARN = aws.ToString(active.TaskDefinitionArn)
			out.Revision = active.Revision
			out.Image = desired.Containers[0].Image
			out.Architecture = desired.Architecture
			return out, nil
		}
	}

	registered, err := api.RegisterTaskDefinition(ctx, registerInput(desired,
		baseapi.CollectorManagedTags(in.Connector, in.ConnectorID, in.Region, in.HostNetworkID)))
	if err != nil {
		return out, fmt.Errorf("register task definition %s: %w", out.Family, err)
	}
	if registered.TaskDefinition == nil {
		return out, fmt.Errorf("register task definition %s returned no definition", out.Family)
	}
	out.ARN = aws.ToString(registered.TaskDefinition.TaskDefinitionArn)
	out.Revision = registered.TaskDefinition.Revision
	out.Registered = true
	out.Image = desired.Containers[0].Image
	out.Architecture = desired.Architecture
	log.Info("registered collector task definition",
		"region", in.Region, "network", in.HostNetworkID, "family", out.Family, "revision", out.Revision,
		"fingerprint", desired.Fingerprint()[:12])
	return out, nil
}

// describeActiveTaskDefinition returns the family's ACTIVE revision, or nil when the family has none.
//
// ECS reports an unknown family as a generic ClientException rather than a typed not-found, so the
// absence is matched on the error code. A family whose revisions were all deregistered describes as
// INACTIVE, which is treated as absent: registering into it is exactly right, and comparing against a
// definition nothing can run is not.
func describeActiveTaskDefinition(ctx context.Context, api TaskDefinitionClient, family string) (*ecstypes.TaskDefinition, []ecstypes.Tag, error) {
	out, err := api.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(family),
		// TAGS has to be asked for, and the tags come back in a field of the OUTPUT rather than on the
		// definition — which is why they are returned separately here. Reading them off the definition
		// would find nothing, every existing family would look untagged, and every one of them would be
		// refused as somebody else's.
		Include: []ecstypes.TaskDefinitionField{ecstypes.TaskDefinitionFieldTags},
	})
	if err != nil {
		if isTaskDefinitionNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("describe task definition %s: %w", family, err)
	}
	if out.TaskDefinition == nil || out.TaskDefinition.Status != ecstypes.TaskDefinitionStatusActive {
		return nil, nil, nil
	}
	return out.TaskDefinition, out.Tags, nil
}

// registerInput turns a spec into the API call. The one place a spec becomes AWS shapes, so the
// round-trip in specFromDefinition has exactly one counterpart to stay in step with.
func registerInput(s TaskDefinitionSpec, tags map[string]string) *ecs.RegisterTaskDefinitionInput {
	containers := make([]ecstypes.ContainerDefinition, 0, len(s.Containers))
	for _, c := range s.Containers {
		ports := make([]ecstypes.PortMapping, 0, len(c.Ports))
		for _, p := range sortedInts(c.Ports) {
			ports = append(ports, ecstypes.PortMapping{
				ContainerPort: aws.Int32(int32(p)),
				// Equal to the container port, which awsvpc requires: the task has its own ENI, so there
				// is no host to map a different port on.
				HostPort: aws.Int32(int32(p)),
				Protocol: ecstypes.TransportProtocolTcp,
			})
		}
		env := make([]ecstypes.KeyValuePair, 0, len(c.Environment))
		for _, k := range sortedKeys(c.Environment) {
			env = append(env, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(c.Environment[k])})
		}
		secrets := make([]ecstypes.Secret, 0, len(c.Secrets))
		for _, k := range sortedKeys(c.Secrets) {
			secrets = append(secrets, ecstypes.Secret{Name: aws.String(k), ValueFrom: aws.String(c.Secrets[k])})
		}
		containers = append(containers, ecstypes.ContainerDefinition{
			Name:  aws.String(c.Name),
			Image: aws.String(c.Image),
			RepositoryCredentials: func() *ecstypes.RepositoryCredentials {
				if c.RepositoryCredentialsRef == "" {
					return nil
				}
				return &ecstypes.RepositoryCredentials{CredentialsParameter: aws.String(c.RepositoryCredentialsRef)}
			}(),
			Essential:    aws.Bool(c.Essential),
			Command:      c.Command,
			PortMappings: ports,
			Environment:  env,
			Secrets:      secrets,
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriver(c.LogDriver),
				Options:   c.LogOptions,
			},
		})
	}
	compat := make([]ecstypes.Compatibility, 0, len(s.RequiresCompatibilities))
	for _, c := range sortedCopy(s.RequiresCompatibilities) {
		compat = append(compat, ecstypes.Compatibility(c))
	}
	return &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(s.Family),
		Cpu:                     aws.String(s.CPU),
		Memory:                  aws.String(s.Memory),
		NetworkMode:             ecstypes.NetworkMode(s.NetworkMode),
		RequiresCompatibilities: compat,
		ExecutionRoleArn:        aws.String(s.ExecutionRoleARN),
		RuntimePlatform: &ecstypes.RuntimePlatform{
			CpuArchitecture:       ecstypes.CPUArchitecture(s.Architecture),
			OperatingSystemFamily: ecstypes.OSFamily(s.OperatingSystemFamily),
		},
		ContainerDefinitions: containers,
		Tags:                 ecsTags(tags),
	}
}

// specFromDefinition decodes a described definition back into the spec shape.
//
// The other half of the material-change test, and the half that has to be exactly right: a field this
// forgets to read comes back as its zero value, differs from the rendered spec, and produces a new
// revision on every pass. taskdefinition_test.go asserts the round trip — render, register, describe,
// decode, fingerprint — lands on the same digest, which is the only way to be sure.
func specFromDefinition(td *ecstypes.TaskDefinition) TaskDefinitionSpec {
	if td == nil {
		return TaskDefinitionSpec{}
	}
	spec := TaskDefinitionSpec{
		Family:           aws.ToString(td.Family),
		CPU:              aws.ToString(td.Cpu),
		Memory:           aws.ToString(td.Memory),
		NetworkMode:      string(td.NetworkMode),
		ExecutionRoleARN: aws.ToString(td.ExecutionRoleArn),
	}
	if td.RuntimePlatform != nil {
		spec.Architecture = string(td.RuntimePlatform.CpuArchitecture)
		spec.OperatingSystemFamily = string(td.RuntimePlatform.OperatingSystemFamily)
	}
	for _, c := range td.RequiresCompatibilities {
		spec.RequiresCompatibilities = append(spec.RequiresCompatibilities, string(c))
	}
	for _, c := range td.ContainerDefinitions {
		container := ContainerSpec{
			Name:        aws.ToString(c.Name),
			Image:       aws.ToString(c.Image),
			Essential:   aws.ToBool(c.Essential),
			Command:     c.Command,
			Environment: map[string]string{},
			Secrets:     map[string]string{},
		}
		if c.RepositoryCredentials != nil {
			container.RepositoryCredentialsRef = aws.ToString(c.RepositoryCredentials.CredentialsParameter)
		}
		for _, p := range c.PortMappings {
			container.Ports = append(container.Ports, int(aws.ToInt32(p.ContainerPort)))
		}
		for _, e := range c.Environment {
			container.Environment[aws.ToString(e.Name)] = aws.ToString(e.Value)
		}
		for _, s := range c.Secrets {
			container.Secrets[aws.ToString(s.Name)] = aws.ToString(s.ValueFrom)
		}
		if c.LogConfiguration != nil {
			container.LogDriver = string(c.LogConfiguration.LogDriver)
			container.LogOptions = c.LogConfiguration.Options
		}
		spec.Containers = append(spec.Containers, container)
	}
	return spec
}

// fargateSize is one CPU value and the memory values Fargate accepts alongside it.
type fargateSize struct {
	cpu int
	// values, when set, is the exact list. Used for 256, whose accepted memory values are not a range.
	values []int
	// min, max and step describe a regular range, in MiB.
	min, max, step int
}

// fargateSizes is the Fargate task-size matrix.
//
// Validated here rather than left to the API, because RegisterTaskDefinition refuses an unsupported
// pair with "No Fargate configuration exists for given values" — which names neither field nor the
// values that would work, and would be the entire diagnosis available on every pass for ever.
var fargateSizes = []fargateSize{
	{cpu: 256, values: []int{512, 1024, 2048}},
	{cpu: 512, min: 1024, max: 4096, step: 1024},
	{cpu: 1024, min: 2048, max: 8192, step: 1024},
	{cpu: 2048, min: 4096, max: 16384, step: 1024},
	{cpu: 4096, min: 8192, max: 30720, step: 1024},
	{cpu: 8192, min: 16384, max: 61440, step: 4096},
	{cpu: 16384, min: 32768, max: 122880, step: 8192},
}

// checkFargateSize reports a cpu/memory pair Fargate does not offer.
func checkFargateSize(region, cpu, memory string) []collectorstatus.Problem {
	refuse := func(msg string) []collectorstatus.Problem {
		return []collectorstatus.Problem{{
			Code:     ProblemTaskSizeInvalid,
			Severity: collectorstatus.SeverityError,
			Message:  fmt.Sprintf("no Collector is deployed in %s: %s", region, msg),
		}}
	}
	cpuValue, err := strconv.Atoi(cpu)
	if err != nil {
		return refuse(fmt.Sprintf("collector.regions.%s.cpu is %q, which is not a number of CPU units. "+
			"Fargate takes %s", region, cpu, describeFargateCPUs()))
	}
	memoryValue, err := strconv.Atoi(memory)
	if err != nil {
		return refuse(fmt.Sprintf("collector.regions.%s.memory is %q, which is not a number of MiB",
			region, memory))
	}
	for _, size := range fargateSizes {
		if size.cpu != cpuValue {
			continue
		}
		if size.memoryAllowed(memoryValue) {
			return nil
		}
		return refuse(fmt.Sprintf("Fargate does not offer %d MiB of memory with %d CPU units; with that CPU "+
			"it offers %s. Set collector.regions.%s.cpu and .memory to a pair Fargate supports — lists "+
			"the three sizes Odigos suggests", memoryValue, cpuValue, size.describe(), region))
	}
	return refuse(fmt.Sprintf("%d is not a Fargate CPU size; Fargate takes %s. Set "+
		"collector.regions.%s.cpu to one of those", cpuValue, describeFargateCPUs(), region))
}

func (s fargateSize) memoryAllowed(memory int) bool {
	if len(s.values) > 0 {
		for _, v := range s.values {
			if v == memory {
				return true
			}
		}
		return false
	}
	if memory < s.min || memory > s.max {
		return false
	}
	return (memory-s.min)%s.step == 0
}

func (s fargateSize) describe() string {
	if len(s.values) > 0 {
		parts := make([]string, 0, len(s.values))
		for _, v := range s.values {
			parts = append(parts, strconv.Itoa(v))
		}
		return strings.Join(parts, ", ") + " MiB"
	}
	return fmt.Sprintf("%d to %d MiB in steps of %d", s.min, s.max, s.step)
}

func describeFargateCPUs() string {
	parts := make([]string, 0, len(fargateSizes))
	for _, s := range fargateSizes {
		parts = append(parts, strconv.Itoa(s.cpu))
	}
	return strings.Join(parts, ", ")
}

// isTaskDefinitionNotFound matches ECS's answer for a family it has never seen. ECS gives it no typed
// form — it is a ClientException whose message names the family — so the code is what is matched, and
// the message deliberately is not.
func isTaskDefinitionNotFound(err error) bool {
	var client *ecstypes.ClientException
	if errors.As(err, &client) {
		return true
	}
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "ClientException"
}

func sortedCopy(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

func sortedInts(xs []int) []int {
	out := append([]int(nil), xs...)
	sort.Ints(out)
	return out
}
