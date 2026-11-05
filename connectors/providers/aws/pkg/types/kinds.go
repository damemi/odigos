// Package types holds AWS connector type name constants used by the provider runtime.
// Values match connectors/spec/data/aws.yaml (features.*.types[].name).
package types

const (
	Lambda         = "aws.lambda"
	AgentCore      = "aws.bedrock-agentcore-runtime"
	FargateTask    = "aws.fargate-task"
	FargateCluster = "aws.fargate-cluster"
	ECSCluster     = "aws.ecs-cluster"
	EKS            = "aws.eks"
	EC2            = "aws.ec2"
)
