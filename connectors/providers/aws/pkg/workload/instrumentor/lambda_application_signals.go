package instrumentor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/applicationsignals"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
)

const lambdaApplicationSignalsPolicy = "CloudWatchLambdaApplicationSignalsExecutionRolePolicy"
const applicationSignalsDiscoveryRole = "AWSServiceRoleForCloudWatchApplicationSignals"

func validateLambdaApplicationSignalsTarget(language, distro string, target domaindest.OTLPTarget) error {
	if recipeFor(language, distro).ApplicationSignals && (!target.Traces || !target.Metrics) {
		return fmt.Errorf("ADOT + Application Signals requires traces and metrics to be enabled for the destination")
	}
	return nil
}

type lambdaApplicationSignalsIAM interface {
	GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
	ListAttachedRolePolicies(context.Context, *iam.ListAttachedRolePoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	AttachRolePolicy(context.Context, *iam.AttachRolePolicyInput, ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error)
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
}

type applicationSignalsDiscovery interface {
	StartDiscovery(context.Context, *applicationsignals.StartDiscoveryInput, ...func(*applicationsignals.Options)) (*applicationsignals.StartDiscoveryOutput, error)
}

// Only the Application Signals preset uses these permissions. The AWS-managed
// policy must already exist. Never create or edit a customer's IAM policy.
func ensureLambdaApplicationSignals(ctx context.Context, identity lambdaApplicationSignalsIAM, discovery applicationSignalsDiscovery, roleARN string) error {
	role, err := arn.Parse(roleARN)
	if err != nil || role.Service != "iam" || !strings.HasPrefix(role.Resource, "role/") || strings.HasSuffix(role.Resource, "/") {
		return fmt.Errorf("Application Signals requires a valid Lambda execution-role ARN")
	}
	policyARN := "arn:" + role.Partition + ":iam::aws:policy/" + lambdaApplicationSignalsPolicy
	if _, err := identity.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(policyARN)}); err != nil {
		return fmt.Errorf("Application Signals requires existing policy %s: %w", policyARN, err)
	}
	// IAM's RoleName excludes its optional path.
	parts := strings.Split(role.Resource, "/")
	name := parts[len(parts)-1]
	attached := false
	var marker *string
	for {
		policies, err := identity.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(name), Marker: marker})
		if err != nil {
			return fmt.Errorf("read Application Signals execution-role policies: %w", err)
		}
		for _, policy := range policies.AttachedPolicies {
			attached = attached || aws.ToString(policy.PolicyArn) == policyARN
		}
		if attached || !policies.IsTruncated {
			break
		}
		if aws.ToString(policies.Marker) == "" || aws.ToString(policies.Marker) == aws.ToString(marker) {
			return fmt.Errorf("IAM returned an invalid execution-role policy pagination marker")
		}
		marker = policies.Marker
	}
	if !attached {
		if _, err := identity.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(name), PolicyArn: aws.String(policyARN)}); err != nil {
			return fmt.Errorf("attach existing Application Signals policy to execution role %s: %w", name, err)
		}
	}
	// Discovery is account-wide and persistent. Its service-linked role is the
	// documented indication that setup has already completed.
	if _, err := identity.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(applicationSignalsDiscoveryRole)}); err != nil {
		var missing *iamtypes.NoSuchEntityException
		if !errors.As(err, &missing) {
			return fmt.Errorf("check Application Signals discovery: %w", err)
		}
		if _, err := discovery.StartDiscovery(ctx, &applicationsignals.StartDiscoveryInput{}); err != nil {
			return fmt.Errorf("enable Application Signals discovery: %w", err)
		}
	}
	return nil
}

func (i *LambdaInstrumentor) ensureApplicationSignals(ctx context.Context, region, roleARN string) error {
	cfg, err := i.cache.Config(ctx, region)
	if err != nil {
		return err
	}
	return ensureLambdaApplicationSignals(ctx, iam.NewFromConfig(cfg), applicationsignals.NewFromConfig(cfg), roleARN)
}
