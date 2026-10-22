package awsprovider

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/deployer"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// ManagedCollectorInstructions renders customer-applied ECS Fargate artifacts for a region. They
// intentionally use the identity and IaC attribution tags but never odigos-managed: applying either
// document leaves ownership, mutation and deletion with the customer's IaC system.
func (p *Provider) ManagedCollectorInstructions(ctx context.Context, region string) (*baseapi.ManagedCollectorInstructions, error) {
	if strings.TrimSpace(region) == "" {
		return nil, nil
	}
	settings := p.cfgMgr.Effective()
	collector, ok := settings["collector"].(map[string]any)
	if !ok {
		return nil, nil
	}
	regions, ok := collector["regions"].(map[string]any)
	if !ok {
		return nil, nil
	}
	if _, ok := regions[region]; !ok {
		return nil, nil
	}

	resolved := p.cfg.Capabilities.CollectorMode(collectorType)
	mode := resolved

	regional := collectorsettings.Read(settings).Region(region)
	if !p.cfg.PlatformIDFromCR {
		return nil, fmt.Errorf("managed Collector instructions require a connector with a unique CR identity")
	}
	imageArch := collectorimage.DefaultArch
	if regional.Arch != "" {
		imageArch = collectorimage.Arch(regional.Arch)
	}
	// Values that are deliberately supplied as parameters are credentials and deployment choices that
	// manual mode must never mint or mutate. The image selection is shared with automatic mode.
	pin, err := collectorimage.Configure(p.cfg.CollectorImage, collectorimage.CloudAWS, regional.Image)
	if err != nil {
		return nil, fmt.Errorf("resolve configured AWS collector image: %w", err)
	}
	image, err := collectorimage.Resolve(pin, collectorimage.CloudAWS, imageArch, collectorimage.Override{})
	if err != nil {
		return nil, fmt.Errorf("resolve configured AWS collector image: %w", err)
	}
	hostNetwork := configuredString(regions[region], "networkId")
	if surface := p.collectors(); surface != nil {
		if plan, _, found := surface.resolver.Plan(); found {
			for _, network := range plan.CollectorNetworks() {
				if network.Region == region && network.NetworkID != "" {
					hostNetwork = network.NetworkID
					break
				}
			}
		}
	}
	if hostNetwork == "" {
		hostNetwork = "<HOST_VPC_ID>"
	}
	cpu, memory := collectorSizing(collector, regions[region])
	scaling := regional.Scaling.Resolve()
	desired := scaling.StartingCount()
	arch := "X86_64"
	if regional.Arch == "arm64" {
		arch = "ARM64"
	}
	securityGroupID := "<COLLECTOR_SECURITY_GROUP_ID>"
	if len(regional.SecurityGroupIDs) > 0 && regional.SecurityGroupIDs[0] != "" {
		securityGroupID = regional.SecurityGroupIDs[0]
	}
	values := collectorInstructionValues(region, hostNetwork, p.cfg.ConnectorName,
		p.cfg.PlatformID, p.cfg.OpAMPEndpoint, image.Ref, cpu, memory, arch, desired, scaling, regional.AssignPublicIP, securityGroupID)
	clusterRef := regional.ClusterRef
	if clusterRef == "" {
		clusterRef = "<CLUSTER_ARN>"
	}
	clusterName := clusterRef
	if index := strings.LastIndex(clusterName, "/"); index >= 0 {
		clusterName = clusterName[index+1:]
	}
	executionRoleARN := regional.ExecutionRoleARN
	if executionRoleARN == "" {
		executionRoleARN = "<EXECUTION_ROLE_ARN>"
	}
	subnetIDs := regional.SubnetIDs
	if len(subnetIDs) == 0 {
		subnetIDs = []string{"<SUBNET_A_ID>", "<SUBNET_B_ID>"}
	}
	credentialSecretARN := "<CREDENTIAL_SECRET_ARN>"
	if p.bstore != nil {
		if record, err := p.bstore.GetManagedCollector(ctx, basestore.CollectorKey{Region: region}); err == nil && record.CredentialRef != "" {
			credentialSecretARN = record.CredentialRef
		}
	}
	values["ClusterRef"] = clusterRef
	values["ClusterName"] = clusterName
	values["ExecutionRoleARN"] = executionRoleARN
	values["SubnetIDs"] = subnetIDs
	values["SubnetIDsCSV"] = strings.Join(subnetIDs, ",")
	values["CredentialSecretARN"] = credentialSecretARN
	values["LicenseSecretName"] = fmt.Sprintf("odigos/collector/%s/%s/license", p.cfg.ConnectorName, region)
	authored := p.Spec().Collectors.Instructions
	documents := make([]baseapi.InstructionDocument, 0, len(authored.Documents))
	for _, document := range authored.Documents {
		rendered, err := renderCollectorDocument(document, values)
		if err != nil {
			return nil, fmt.Errorf("render AWS Collector %s instructions: %w", document.Variant, err)
		}
		documents = append(documents, rendered)
	}
	notes := make([]string, 0, len(authored.Notes))
	for _, note := range authored.Notes {
		rendered, err := renderCollectorText(note, values)
		if err != nil {
			return nil, fmt.Errorf("render AWS Collector note: %w", err)
		}
		notes = append(notes, rendered)
	}

	return &baseapi.ManagedCollectorInstructions{
		Region: region, HostNetworkID: hostNetwork, Mode: mode, UpdatedAt: time.Now(),
		Documents: documents, Notes: notes,
	}, nil
}

func configuredString(raw any, key string) string {
	m, _ := raw.(map[string]any)
	v, _ := m[key].(string)
	return v
}

func collectorSizing(defaults map[string]any, raw any) (string, string) {
	cpu, memory := "1024", "2048"
	if d, ok := defaults["defaults"].(map[string]any); ok {
		if v, ok := d["cpu"].(string); ok && v != "" {
			cpu = v
		}
		if v, ok := d["memory"].(string); ok && v != "" {
			memory = v
		}
	}
	if r, ok := raw.(map[string]any); ok {
		if v, ok := r["cpu"].(string); ok && v != "" {
			cpu = v
		}
		if v, ok := r["memory"].(string); ok && v != "" {
			memory = v
		}
	}
	return cpu, memory
}

func artifactInputs(content string) []baseapi.InstructionInput {
	candidates := []baseapi.InstructionInput{
		{Placeholder: "<HOST_VPC_ID>", Description: "The VPC where you will deploy this regional Collector."},
		{Placeholder: "<ODIGOS_CONNECTOR_NAME>", Description: "The name of this Odigos AWS connector."},
		{Placeholder: "<ODIGOS_CONNECTOR_ID>", Description: "The CR UID of this Odigos AWS connector."},
		{Placeholder: "<OPAMP_ENDPOINT>", Description: "The public OpAMP endpoint configured for this connector."},
		{Placeholder: "<CLUSTER_ARN>", Description: "The existing ECS Fargate cluster ARN."},
		{Placeholder: "<CLUSTER_NAME>", Description: "The name of that ECS cluster, used for autoscaling."},
		{Placeholder: "<EXECUTION_ROLE_ARN>", Description: "The customer-owned task execution role ARN."},
		{Placeholder: "<SUBNET_A_ID>", Description: "A Collector subnet in the host VPC."},
		{Placeholder: "<SUBNET_B_ID>", Description: "A Collector subnet in a second availability zone."},
		{Placeholder: "<COLLECTOR_SECURITY_GROUP_ID>", Description: "The Collector security group created during prerequisites."},
		{Placeholder: "<LICENSE_SECRET_ARN>", Description: "The registry licence secret ARN."},
		{Placeholder: "<CREDENTIAL_SECRET_ARN>", Description: "The one-time OpAMP credential secret ARN."},
	}
	var unresolved []baseapi.InstructionInput
	for _, candidate := range candidates {
		if strings.Contains(content, candidate.Placeholder) {
			unresolved = append(unresolved, candidate)
		}
	}
	return unresolved
}

// collectorInstructionValues contains only runtime facts. All customer-facing document
// structure, prose and scaling branches are authored in spec/data/aws.yaml.
func collectorInstructionValues(region, network, connector, connectorID, opampEndpoint, image, cpu, memory, arch string, desired int, scaling collectorsettings.ResolvedScaling, assignPublic bool, securityGroupID string) map[string]any {
	if connector == "" {
		connector = "<ODIGOS_CONNECTOR_NAME>"
	}
	if connectorID == "" {
		connectorID = "<ODIGOS_CONNECTOR_ID>"
	}
	if opampEndpoint == "" {
		opampEndpoint = "<OPAMP_ENDPOINT>"
	}
	publicIP := "DISABLED"
	if assignPublic {
		publicIP = "ENABLED"
	}
	cpuPolicy, memoryPolicy := deployer.ScalingPolicyNames(region)
	return map[string]any{
		"Region": region, "Network": network,
		"SecurityGroupID": securityGroupID,
		"ClusterRef":      "<CLUSTER_ARN>", "ClusterName": "<CLUSTER_NAME>",
		"ExecutionRoleARN":    "<EXECUTION_ROLE_ARN>",
		"SubnetIDs":           []string{"<SUBNET_A_ID>", "<SUBNET_B_ID>"},
		"SubnetIDsCSV":        "<SUBNET_A_ID>,<SUBNET_B_ID>",
		"CredentialSecretARN": "<CREDENTIAL_SECRET_ARN>",
		"LicenseSecretName":   fmt.Sprintf("odigos/collector/%s/%s/license", connector, region),
		"ConnectorName":       connector, "ConnectorID": connectorID, "OpAMPEndpoint": opampEndpoint,
		"Image": image, "CPU": cpu, "Memory": memory, "Arch": arch,
		"PublicECRImage": collectorimage.IsPublicECR(image),
		"DesiredCount":   desired, "AssignPublicIP": assignPublic, "PublicIPString": publicIP,
		"TaskDefinitionFamily": deployer.TaskDefinitionFamily(region),
		"ServiceName":          deployer.ServiceName(region), "LoadBalancerName": deployer.LoadBalancerName(network),
		"TargetGroupGRPC":  deployer.TargetGroupName(network, 4317),
		"TargetGroupHTTP":  deployer.TargetGroupName(network, 4318),
		"LogGroupName":     deployer.LogGroupName(connector),
		"ScalingAutomatic": scaling.Automatic(), "ScalingMin": scaling.MinCount, "ScalingMax": scaling.MaxCount,
		"TargetCPUPercent": scaling.TargetCPUPercent, "TargetMemoryPercent": scaling.TargetMemoryPercent,
		"CPUPolicyName": cpuPolicy, "MemoryPolicyName": memoryPolicy,
		"TerraformResourceID": "service/${var.cluster_name}/" + deployer.ServiceName(region),
	}
}

func renderCollectorText(authored string, values map[string]any) (string, error) {
	parsed, err := template.New("collector-instruction").Funcs(template.FuncMap{
		"quote": strconv.Quote,
	}).Option("missingkey=error").Parse(authored)
	if err != nil {
		return "", err
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, values); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func renderCollectorDocument(authored spec.CollectorInstructionDocument, values map[string]any) (baseapi.InstructionDocument, error) {
	content, err := renderCollectorText(authored.Template, values)
	if err != nil {
		return baseapi.InstructionDocument{}, fmt.Errorf("content: %w", err)
	}
	steps := make([]string, 0, len(authored.Steps))
	for _, step := range authored.Steps {
		rendered, err := renderCollectorText(step, values)
		if err != nil {
			return baseapi.InstructionDocument{}, fmt.Errorf("step: %w", err)
		}
		steps = append(steps, rendered)
	}
	title, err := renderCollectorText(authored.Title, values)
	if err != nil {
		return baseapi.InstructionDocument{}, fmt.Errorf("title: %w", err)
	}
	description, err := renderCollectorText(authored.Description, values)
	if err != nil {
		return baseapi.InstructionDocument{}, fmt.Errorf("description: %w", err)
	}
	return baseapi.InstructionDocument{
		ID: authored.ID, Variant: authored.Variant, VariantLabel: authored.VariantLabel,
		Title: title, Description: description,
		Steps: steps, Format: authored.Format, Content: content, Unresolved: artifactInputs(content),
	}, nil
}
