package awsprovider

import (
	"context"
	"fmt"
	"strings"
	"time"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/deployer"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

// ManagedCollectorInstructions renders customer-applied ECS Fargate artifacts for a region. They
// intentionally use the identity and IaC attribution tags but never odigos-managed: applying either
// document leaves ownership, mutation and deletion with the customer's IaC system.
func (p *Provider) ManagedCollectorInstructions(_ context.Context, region string) (*baseapi.ManagedCollectorInstructions, error) {
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
	imageArch := collectorimage.DefaultArch
	if regional.Arch != "" {
		imageArch = collectorimage.Arch(regional.Arch)
	}
	// Values that are deliberately supplied as parameters are credentials and deployment choices that
	// manual mode must never mint or mutate. The image is the released digest used by automatic mode.
	image, err := collectorimage.Resolve(collectorimage.Released, collectorimage.CloudAWS, imageArch, collectorimage.Override{})
	if err != nil {
		return nil, fmt.Errorf("resolve released AWS collector image: %w", err)
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

	return &baseapi.ManagedCollectorInstructions{
		Region: region, HostNetworkID: hostNetwork, DeploymentRoleRef: regional.DeploymentRoleRef, Mode: mode, UpdatedAt: time.Now(),
		Documents: []baseapi.InstructionDocument{
			awsCloudFormationDocument(region, hostNetwork, regional.DeploymentRoleRef, p.cfg.ConnectorName, p.cfg.PlatformID, p.cfg.OpAMPEndpoint, image.Ref, cpu, memory, arch, desired, scaling),
			awsTerraformDocument(region, hostNetwork, regional.DeploymentRoleRef, p.cfg.ConnectorName, p.cfg.PlatformID, p.cfg.OpAMPEndpoint, image.Ref, cpu, memory, arch, desired, scaling),
		},
		Notes: []string{
			"Use the customer-provisioned deployment role " + regional.DeploymentRoleRef + " for this Collector. In automatic mode the connector assumes it; in manual mode apply the infrastructure documents with credentials from that role.",
			"These artifacts are customer-owned. They set odigos-iac for attribution and deliberately omit odigos-managed, so Odigos observes the Collector and never deletes or converges these resources.",
			"Provide separate Secrets Manager values for ODIGOS_ONPREM_TOKEN and ODIGOS_OPAMP_AUTHORIZATION. The authorization value must include its scheme (for example, Bearer …); do not put either secret in task-definition environment values.",
			"Allow TCP " + fmt.Sprintf("%d and %d", collectoringress.PortOTLPGRPC, collectoringress.PortOTLPHTTP) + " from the security groups or CIDRs of every workload that exports telemetry. Because the NLB and tasks share a security group, also allow TCP 13133 from that same group for target health checks. The task needs egress to the OpAMP endpoint and destination endpoints.",
		},
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
	}
	var unresolved []baseapi.InstructionInput
	for _, candidate := range candidates {
		if strings.Contains(content, candidate.Placeholder) {
			unresolved = append(unresolved, candidate)
		}
	}
	return unresolved
}

func awsCloudFormationDocument(region, network, deploymentRole, connector, connectorID, opampEndpoint, image, cpu, memory, arch string, desired int, scaling collectorsettings.ResolvedScaling) baseapi.InstructionDocument {
	if connector == "" {
		connector = "<ODIGOS_CONNECTOR_NAME>"
	}
	if connectorID == "" {
		connectorID = "<ODIGOS_CONNECTOR_ID>"
	}
	if opampEndpoint == "" {
		opampEndpoint = "<OPAMP_ENDPOINT>"
	}
	content := fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: Customer-owned Odigos regional Collector for %s
Parameters:
  ClusterArn: { Type: String }
  ClusterName: { Type: String }
  ExecutionRoleArn: { Type: String }
  CloudWatchLogGroup: { Type: String }
  ApplicationAutoscalingRoleArn: { Type: String, Default: '' }
  SubnetIds: { Type: 'List<AWS::EC2::Subnet::Id>' }
  SecurityGroupId: { Type: 'AWS::EC2::SecurityGroup::Id' }
  LicenseSecretArn: { Type: String, NoEcho: true }
  OpampAuthorizationSecretArn: { Type: String, NoEcho: true }
Resources:
  CollectorTaskDefinition:
    Type: AWS::ECS::TaskDefinition
    Properties:
      Family: %s
      Cpu: '%s'
      Memory: '%s'
      NetworkMode: awsvpc
      RequiresCompatibilities: [FARGATE]
      RuntimePlatform: { OperatingSystemFamily: LINUX, CpuArchitecture: %s }
      ExecutionRoleArn: !Ref ExecutionRoleArn
      ContainerDefinitions:
        - Name: collector
          Image: %s
          Essential: true
          Command: [/odigosopampsupervisor, --config=/etc/odigos/managed-collector/supervisor.yaml]
          PortMappings: [{ContainerPort: 4317}, {ContainerPort: 4318}, {ContainerPort: 13133}, {ContainerPort: 13134}]
          Environment:
            - { Name: ODIGOS_CLOUD_PROVIDER, Value: aws }
            - { Name: ODIGOS_CLOUD_CONNECTOR_ID, Value: '%s' }
            - { Name: ODIGOS_OPAMP_ENDPOINT, Value: '%s' }
          Secrets:
            - { Name: ODIGOS_ONPREM_TOKEN, ValueFrom: !Ref LicenseSecretArn }
            - { Name: ODIGOS_OPAMP_AUTHORIZATION, ValueFrom: !Ref OpampAuthorizationSecretArn }
          LogConfiguration: { LogDriver: awslogs, Options: { awslogs-group: !Ref CloudWatchLogGroup, awslogs-region: %s, awslogs-stream-prefix: collector } }
      Tags:
        - { Key: odigos-component, Value: collector }
        - { Key: odigos-connector, Value: %s }
        - { Key: odigos-connector-id, Value: %s }
        - { Key: odigos-region, Value: %s }
        - { Key: odigos-network, Value: %s }
        - { Key: odigos-iac, Value: customer }
  CollectorService:
    Type: AWS::ECS::Service
    DependsOn: [GrpcListener, HttpListener]
    Properties:
      Cluster: !Ref ClusterArn
      ServiceName: %s
      TaskDefinition: !Ref CollectorTaskDefinition
      LaunchType: FARGATE
      DesiredCount: %d
      LoadBalancers:
        - { ContainerName: collector, ContainerPort: 4317, TargetGroupArn: !Ref GrpcTargetGroup }
        - { ContainerName: collector, ContainerPort: 4318, TargetGroupArn: !Ref HttpTargetGroup }
      NetworkConfiguration: { AwsvpcConfiguration: { AssignPublicIp: DISABLED, Subnets: !Ref SubnetIds, SecurityGroups: [!Ref SecurityGroupId] } }
      Tags:
        - { Key: odigos-component, Value: collector }
        - { Key: odigos-connector, Value: %s }
        - { Key: odigos-connector-id, Value: %s }
        - { Key: odigos-region, Value: %s }
        - { Key: odigos-network, Value: %s }
        - { Key: odigos-iac, Value: customer }

  CollectorNlb:
    Type: AWS::ElasticLoadBalancingV2::LoadBalancer
    Properties: { Name: %s, Type: network, Scheme: internal, Subnets: !Ref SubnetIds, SecurityGroups: [!Ref SecurityGroupId] }
  GrpcTargetGroup:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
    Properties: { Name: %s, Port: 4317, Protocol: TCP, TargetType: ip, VpcId: '%s', HealthCheckPort: '13133', HealthCheckProtocol: TCP, TargetGroupAttributes: [{Key: preserve_client_ip.enabled, Value: 'true'}, {Key: load_balancing.cross_zone.enabled, Value: 'true'}] }
  HttpTargetGroup:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
    Properties: { Name: %s, Port: 4318, Protocol: TCP, TargetType: ip, VpcId: '%s', HealthCheckPort: '13133', HealthCheckProtocol: TCP, TargetGroupAttributes: [{Key: preserve_client_ip.enabled, Value: 'true'}, {Key: load_balancing.cross_zone.enabled, Value: 'true'}] }
  GrpcListener:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties: { LoadBalancerArn: !Ref CollectorNlb, Port: 4317, Protocol: TCP, DefaultActions: [{Type: forward, TargetGroupArn: !Ref GrpcTargetGroup}] }
  HttpListener:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties: { LoadBalancerArn: !Ref CollectorNlb, Port: 4318, Protocol: TCP, DefaultActions: [{Type: forward, TargetGroupArn: !Ref HttpTargetGroup}] }
Outputs:
  OtlpGrpcEndpoint: { Value: !Sub '${CollectorNlb.DNSName}:4317' }
  OtlpHttpEndpoint: { Value: !Sub 'http://${CollectorNlb.DNSName}:4318' }
`, region, deployer.TaskDefinitionFamily(region), cpu, memory, arch, image, connectorID, opampEndpoint, region, connector, connectorID, region, network, deployer.ServiceName(region), desired, connector, connectorID, region, network, deployer.LoadBalancerName(network), deployer.TargetGroupName(network, 4317), network, deployer.TargetGroupName(network, 4318), network)
	if scaling.Automatic() {
		cpuPolicy, memoryPolicy := deployer.ScalingPolicyNames(region)
		autoscaling := fmt.Sprintf(`
  CollectorScalableTarget:
    Type: AWS::ApplicationAutoScaling::ScalableTarget
    Properties: { MaxCapacity: %d, MinCapacity: %d, ResourceId: !Sub 'service/${ClusterName}/%s', RoleARN: !Ref ApplicationAutoscalingRoleArn, ScalableDimension: 'ecs:service:DesiredCount', ServiceNamespace: ecs }
  CollectorCpuPolicy:
    Type: AWS::ApplicationAutoScaling::ScalingPolicy
    Properties: { PolicyName: %q, PolicyType: TargetTrackingScaling, ScalingTargetId: !Ref CollectorScalableTarget, TargetTrackingScalingPolicyConfiguration: { PredefinedMetricSpecification: { PredefinedMetricType: ECSServiceAverageCPUUtilization }, TargetValue: %d } }
  CollectorMemoryPolicy:
    Type: AWS::ApplicationAutoScaling::ScalingPolicy
    Properties: { PolicyName: %q, PolicyType: TargetTrackingScaling, ScalingTargetId: !Ref CollectorScalableTarget, TargetTrackingScalingPolicyConfiguration: { PredefinedMetricSpecification: { PredefinedMetricType: ECSServiceAverageMemoryUtilization }, TargetValue: %d } }
`, scaling.MaxCount, scaling.MinCount, deployer.ServiceName(region), cpuPolicy, scaling.TargetCPUPercent, memoryPolicy, scaling.TargetMemoryPercent)
		content = strings.Replace(content, "\nOutputs:", autoscaling+"\nOutputs:", 1)
	}
	return baseapi.InstructionDocument{ID: "aws-fargate-collector-cloudformation", Variant: "cloudformation", VariantLabel: "CloudFormation", Title: "Deploy the regional Collector", Description: "Deploy this customer-owned ECS Fargate service in " + region + ".", Steps: []string{"Assume the customer-provisioned deployment role " + deploymentRole + " before applying the stack.", "Supply the referenced credentials as Secrets Manager ARNs, and provide the existing cluster, log group, security group and subnets. Provide an application autoscaling role when scaling mode is auto.", "Point collector.regions." + region + ".endpointOverride at the published NLB endpoint."}, Format: "yaml", Content: content, Unresolved: artifactInputs(content)}
}

func awsTerraformDocument(region, network, deploymentRole, connector, connectorID, opampEndpoint, image, cpu, memory, arch string, desired int, scaling collectorsettings.ResolvedScaling) baseapi.InstructionDocument {
	if connector == "" {
		connector = "<ODIGOS_CONNECTOR_NAME>"
	}
	if connectorID == "" {
		connectorID = "<ODIGOS_CONNECTOR_ID>"
	}
	if opampEndpoint == "" {
		opampEndpoint = "<OPAMP_ENDPOINT>"
	}
	content := fmt.Sprintf(`variable "cluster_arn" { type = string }
variable "cluster_name" { type = string }
variable "execution_role_arn" { type = string }
variable "cloudwatch_log_group" { type = string }
variable "subnet_ids" { type = list(string) }
variable "security_group_ids" { type = list(string) }
variable "license_secret_arn" {
  type = string
  sensitive = true
}
variable "opamp_authorization_secret_arn" {
  type = string
  sensitive = true
}

locals {
  collector_tags = {
    "odigos-component" = "collector"
    "odigos-connector" = %q
    "odigos-connector-id" = %q
    "odigos-region" = %q
    "odigos-network" = %q
    "odigos-iac" = "customer"
  }
}
resource "aws_ecs_task_definition" "collector" {
  family = %q
  cpu = %q
  memory = %q
  network_mode = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  execution_role_arn = var.execution_role_arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture = %q
  }
  container_definitions = jsonencode([{
    name = "collector"
    image = %q
    essential = true
    command = ["/odigosopampsupervisor", "--config=/etc/odigos/managed-collector/supervisor.yaml"]
    portMappings = [for p in [4317, 4318, 13133, 13134] : { containerPort = p }]
    environment = [{ name = "ODIGOS_CLOUD_PROVIDER", value = "aws" }, { name = "ODIGOS_CLOUD_CONNECTOR_ID", value = %q }, { name = "ODIGOS_OPAMP_ENDPOINT", value = %q }]
    secrets = [{ name = "ODIGOS_ONPREM_TOKEN", valueFrom = var.license_secret_arn }, { name = "ODIGOS_OPAMP_AUTHORIZATION", valueFrom = var.opamp_authorization_secret_arn }]
    logConfiguration = { logDriver = "awslogs", options = { awslogs-group = var.cloudwatch_log_group, awslogs-region = %q, awslogs-stream-prefix = "collector" } }
  }])
  tags = local.collector_tags
}
resource "aws_ecs_service" "collector" {
  name = %q
  cluster = var.cluster_arn
  task_definition = aws_ecs_task_definition.collector.arn
  desired_count = %d
  launch_type = "FARGATE"
  depends_on = [aws_lb_listener.grpc, aws_lb_listener.http]
  load_balancer {
    target_group_arn = aws_lb_target_group.grpc.arn
    container_name = "collector"
    container_port = 4317
  }
  load_balancer {
    target_group_arn = aws_lb_target_group.http.arn
    container_name = "collector"
    container_port = 4318
  }
  network_configuration {
    subnets = var.subnet_ids
    security_groups = var.security_group_ids
    assign_public_ip = false
  }
  tags = local.collector_tags
}
resource "aws_lb" "collector" {
  name = %q
  internal = true
  load_balancer_type = "network"
  subnets = var.subnet_ids
  security_groups = var.security_group_ids
  tags = local.collector_tags
}
resource "aws_lb_target_group" "grpc" {
  name = %q
  port = 4317
  protocol = "TCP"
  target_type = "ip"
  vpc_id = %q
  preserve_client_ip = true
  load_balancing_cross_zone_enabled = "true"
  health_check {
    protocol = "TCP"
    port = "13133"
  }
}
resource "aws_lb_target_group" "http" {
  name = %q
  port = 4318
  protocol = "TCP"
  target_type = "ip"
  vpc_id = %q
  preserve_client_ip = true
  load_balancing_cross_zone_enabled = "true"
  health_check {
    protocol = "TCP"
    port = "13133"
  }
}
resource "aws_lb_listener" "grpc" {
  load_balancer_arn = aws_lb.collector.arn
  port = 4317
  protocol = "TCP"
  default_action {
    type = "forward"
    target_group_arn = aws_lb_target_group.grpc.arn
  }
}
resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.collector.arn
  port = 4318
  protocol = "TCP"
  default_action {
    type = "forward"
    target_group_arn = aws_lb_target_group.http.arn
  }
}
output "otlp_grpc_endpoint" { value = "${aws_lb.collector.dns_name}:4317" }
output "otlp_http_endpoint" { value = "http://${aws_lb.collector.dns_name}:4318" }
`, connector, connectorID, region, network, deployer.TaskDefinitionFamily(region), cpu, memory, arch, image, connectorID, opampEndpoint, region, deployer.ServiceName(region), desired, deployer.LoadBalancerName(network), deployer.TargetGroupName(network, 4317), network, deployer.TargetGroupName(network, 4318), network)
	if scaling.Automatic() {
		resourceID := "service/${var.cluster_name}/" + deployer.ServiceName(region)
		cpuPolicy, memoryPolicy := deployer.ScalingPolicyNames(region)
		content += fmt.Sprintf(`
resource "aws_appautoscaling_target" "collector" {
  max_capacity = %d
  min_capacity = %d
  resource_id = %q
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace = "ecs"
}
resource "aws_appautoscaling_policy" "cpu" {
  name = %q
  policy_type = "TargetTrackingScaling"
  resource_id = aws_appautoscaling_target.collector.resource_id
  scalable_dimension = aws_appautoscaling_target.collector.scalable_dimension
  service_namespace = aws_appautoscaling_target.collector.service_namespace
  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value = %d
  }
}
resource "aws_appautoscaling_policy" "memory" {
  name = %q
  policy_type = "TargetTrackingScaling"
  resource_id = aws_appautoscaling_target.collector.resource_id
  scalable_dimension = aws_appautoscaling_target.collector.scalable_dimension
  service_namespace = aws_appautoscaling_target.collector.service_namespace
  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageMemoryUtilization"
    }
    target_value = %d
  }
}
`, scaling.MaxCount, scaling.MinCount, resourceID, cpuPolicy, scaling.TargetCPUPercent, memoryPolicy, scaling.TargetMemoryPercent)
	}
	return baseapi.InstructionDocument{ID: "aws-fargate-collector-terraform", Variant: "terraform", VariantLabel: "Terraform", Title: "Deploy the regional Collector", Description: "Apply this customer-owned ECS Fargate service in " + region + ".", Steps: []string{"Configure the AWS Terraform provider to assume the customer-provisioned deployment role " + deploymentRole + ".", "Set each variable from resources your Terraform workspace owns.", "Keep odigos-iac and the identity tags; do not add odigos-managed.", "Use aws_appautoscaling_target and target tracking policies if collector.regions." + region + ".scaling.mode is auto; otherwise set desired_count to its configured manual value."}, Format: "hcl", Content: content, Unresolved: artifactInputs(content)}
}
