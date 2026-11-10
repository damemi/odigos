// Package awsregions holds service availability shared by discovery and onboarding probes.
package awsregions

import "slices"

// AgentCoreRuntimeSupported follows the Runtime microVM row, verified 2026-10-28:
// https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/agentcore-regions.html
// Do not use the older General Reference table (which still lists nine regions).
func AgentCoreRuntimeSupported(region string) bool {
	return slices.Contains([]string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-central-1", "eu-west-1", "eu-west-2", "eu-south-1", "eu-west-3", "eu-south-2", "eu-north-1",
		"ap-southeast-5", "ap-south-1", "ap-south-2", "ap-southeast-1", "ap-southeast-2", "ap-southeast-7",
		"ap-northeast-1", "ap-northeast-2", "ca-central-1", "sa-east-1", "us-gov-west-1",
	}, region)
}
