package testconnection

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// defaultRegion is used for partition-wide calls (identity, region enumeration); always enabled in the
// commercial partition.
const defaultRegion = "us-east-1"

// Discovery type names, matching the discovery types declared in connectors/spec/data/aws.yaml.
// The three ECS types share the same discovery probe (ecs:ListClusters), so the tester reports a single
// ECS discovery check when any is enabled.
const (
	typeLambda         = "aws.lambda"
	typeFargateTask    = "aws.fargate-task"
	typeFargateCluster = "aws.fargate-cluster"
	typeECSCluster     = "aws.ecs-cluster"
)

// Auth method ids, matching auth.methods[].id in connectors/spec/data/aws.yaml. The connector spec
// resolves which method(s) a credential set selects; the tester switches on these to wire the SDK.
const (
	authWorkloadIdentity = "workload-identity"
	authStaticKeys       = "static-keys"
	authAssumeRole       = "assume-role"
)

// AWS connection check names.
const (
	CheckAuthIdentity    CheckName = "auth_identity"
	CheckAccountMatch    CheckName = "account_match"
	CheckRegionAccess    CheckName = "region_access"
	CheckDiscoveryLambda CheckName = "discovery_lambda"
	CheckDiscoveryECS    CheckName = "discovery_ecs"
)

func init() { Register(awsTester{}) }

// awsTester validates AWS connector credentials with read-only probes (identity, account match, region
// access, discovery permissions) using in-memory credentials only.
type awsTester struct{}

func (awsTester) Provider() string { return "aws" }

// Test executes the read-only checks and returns a per-check report. It returns a non-nil error only
// for unexpected internal failures; expected outcomes (bad credentials, missing permissions) are
// reported as failed checks so the UI can render per-check guidance.
func (awsTester) Test(ctx context.Context, in Input) (*Result, error) {
	res := &Result{}
	awsSpec := spec.MustGet("aws")

	// Resolve which auth method the provided credentials select, validated against the spec's auth
	// definition (required fields, composition). This rejects incomplete/ambiguous credential sets
	// before contacting AWS.
	chain, err := awsSpec.Auth.Resolve(in.Credentials)
	if err != nil {
		res.add(CheckResult{
			Name: CheckAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     err.Error(),
			Remediation: "provide a valid set of credentials for one of the supported authentication methods",
		})
		res.finalize()
		return res, nil
	}

	base, err := buildConfig(ctx, defaultRegion, in.Credentials, chain)
	if err != nil {
		res.add(CheckResult{
			Name: CheckAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     "failed to initialize AWS credentials",
			Remediation: "verify the provided access keys or role ARN",
		})
		res.finalize()
		return res, nil
	}

	// 1. Authentication identity.
	ident, err := sts.NewFromConfig(base).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		res.add(CheckResult{
			Name: CheckAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     summarizeErr(err),
			Remediation: "ensure the credentials are valid and, for a role, that it is assumable with the given external ID",
		})
		res.finalize()
		return res, nil
	}
	res.AccountID = aws.ToString(ident.Account)
	res.Identity = aws.ToString(ident.Arn)
	res.add(CheckResult{
		Name: CheckAuthIdentity, DisplayName: "Authentication", Passed: true,
		Message: fmt.Sprintf("authenticated as %s (via %s)", aws.ToString(ident.Arn), authMethodNames(chain)),
	})

	// 2. Account match.
	if in.ExpectedAccountID != "" {
		match := in.ExpectedAccountID == res.AccountID
		c := CheckResult{Name: CheckAccountMatch, DisplayName: "Account match", Passed: match}
		if match {
			c.Message = fmt.Sprintf("credentials belong to account %s", res.AccountID)
		} else {
			c.Message = fmt.Sprintf("credentials belong to account %s, expected %s", res.AccountID, in.ExpectedAccountID)
			c.Remediation = "use credentials for the configured account, or correct the configured account id"
		}
		res.add(c)
	}

	// Only probe discovery if the customer enabled at least one discovery capability.
	active := map[string]bool{}
	for _, t := range awsSpec.ActiveDiscoveryTypes(in.Capabilities) {
		active[t.Name] = true
	}
	if len(active) == 0 {
		res.finalize()
		return res, nil
	}

	// 3. Region enumeration (validates ec2:DescribeRegions) — the connector scans every enabled region.
	regions, err := enabledRegions(ctx, base)
	if err != nil {
		fail := "could not enumerate enabled regions: " + summarizeErr(err)
		remediation := "verify connectivity to the AWS API"
		if isAuthErr(err) {
			fail = "access denied enumerating enabled regions"
			remediation = "grant ec2:DescribeRegions"
		}
		res.add(boolCheck(CheckRegionAccess, "Region access", false, "", fail, remediation))
		res.finalize()
		return res, nil
	}
	res.add(boolCheck(CheckRegionAccess, "Region access", true,
		fmt.Sprintf("enumerated %d enabled region(s)", len(regions)), "", ""))

	// 4. Per active discovery type, probe across all enabled regions. The ECS types share the same
	// ecs:ListClusters probe (AWS has no launch-type-filtered list API), so probe ECS once when any is
	// enabled and report a single ECS discovery check.
	ecsActive := active[typeFargateTask] || active[typeFargateCluster] || active[typeECSCluster]
	var lambdaDenied, ecsDenied []string
	for _, region := range regions {
		rcfg, err := buildConfig(ctx, region, in.Credentials, chain)
		if err != nil {
			continue
		}
		if active[typeLambda] {
			if _, err := lambda.NewFromConfig(rcfg).ListFunctions(ctx, &lambda.ListFunctionsInput{MaxItems: aws.Int32(1)}); err != nil && isAuthErr(err) {
				lambdaDenied = append(lambdaDenied, region)
			}
		}
		if ecsActive {
			if _, err := ecs.NewFromConfig(rcfg).ListClusters(ctx, &ecs.ListClustersInput{MaxResults: aws.Int32(1)}); err != nil && isAuthErr(err) {
				ecsDenied = append(ecsDenied, region)
			}
		}
	}

	if active[typeLambda] {
		res.add(boolCheck(CheckDiscoveryLambda, "Lambda discovery permissions", len(lambdaDenied) == 0,
			"lambda:ListFunctions allowed",
			"access denied in region(s): "+strings.Join(lambdaDenied, ", "),
			"grant "+permissionsFor(awsSpec, in.Capabilities, "lambda:")))
	}
	if ecsActive {
		// The remediation lists every capability-required ecs:* permission; when Fargate instrumentation
		// is enabled that set includes the write path (RegisterTaskDefinition/UpdateService), which also
		// needs pass-role on the task definitions' roles — called out here since it is never probed
		// (these checks stay read-only).
		ecsGrant := "grant " + permissionsFor(awsSpec, in.Capabilities, "ecs:")
		if iamPerms := permissionsFor(awsSpec, in.Capabilities, "iam:"); !strings.HasPrefix(iamPerms, "the required ") {
			ecsGrant += "; for Fargate instrumentation also grant " + iamPerms +
				" on the task/execution roles (scope with iam:PassedToService=ecs-tasks.amazonaws.com)"
		}
		res.add(boolCheck(CheckDiscoveryECS, "ECS discovery permissions", len(ecsDenied) == 0,
			"ecs:ListClusters allowed",
			"access denied in region(s): "+strings.Join(ecsDenied, ", "),
			ecsGrant))
	}

	res.finalize()
	return res, nil
}

// enabledRegions returns the regions enabled (opted-in) for the account via ec2:DescribeRegions.
func enabledRegions(ctx context.Context, cfg aws.Config) ([]string, error) {
	out, err := ec2.NewFromConfig(cfg).DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return nil, err
	}
	regions := make([]string, 0, len(out.Regions))
	for _, r := range out.Regions {
		if n := aws.ToString(r.RegionName); n != "" {
			regions = append(regions, n)
		}
	}
	return regions, nil
}

// Credential keys, matching the auth field names declared in connectors/spec/data/aws.yaml.
const (
	credAccessKeyID     = "accessKeyId"
	credSecretAccessKey = "secretAccessKey"
	credSessionToken    = "sessionToken"
	credRoleARN         = "roleArn"
	credExternalID      = "externalId"
)

// buildConfig wires an aws.Config from the resolved auth chain (base-first), switching on the spec auth
// method id: static-keys installs a static credential provider, assume-role layers an AssumeRole
// provider on top, workload-identity uses the ambient/default chain. The chain is produced by
// spec AuthConfig.Resolve, so the SDK wiring stays in lockstep with the connector spec's auth methods.
func buildConfig(ctx context.Context, region string, creds map[string]string, chain []spec.AuthMethod) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	assumeRole := false
	for _, m := range chain {
		switch m.ID {
		case authStaticKeys:
			opts = append(opts, config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(creds[credAccessKeyID], creds[credSecretAccessKey], creds[credSessionToken])))
		case authAssumeRole:
			assumeRole = true
		case authWorkloadIdentity:
			// ambient/default credential chain; nothing to add.
		}
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, err
	}
	if assumeRole {
		provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), creds[credRoleARN], func(o *stscreds.AssumeRoleOptions) {
			if ext := creds[credExternalID]; ext != "" {
				o.ExternalID = aws.String(ext)
			}
		})
		cfg.Credentials = aws.NewCredentialsCache(provider)
	}
	return cfg, nil
}

// authMethodNames joins the resolved auth chain's display names for the authentication check message.
func authMethodNames(chain []spec.AuthMethod) string {
	names := make([]string, len(chain))
	for i, m := range chain {
		names[i] = m.DisplayName
	}
	return strings.Join(names, " + ")
}

func boolCheck(name CheckName, display string, ok bool, okMsg, failMsg, remediation string) CheckResult {
	c := CheckResult{Name: name, DisplayName: display, Passed: ok}
	if ok {
		c.Message = okMsg
	} else {
		c.Message = failMsg
		c.Remediation = remediation
	}
	return c
}

// permissionsFor returns the comma-joined permission strings of a given prefix that the requested
// capabilities require (AWS uses "service:" prefixes, e.g. "lambda:").
func permissionsFor(s spec.CloudConnectorSpec, caps spec.Capabilities, prefix string) string {
	var matched []string
	for _, p := range s.RequiredPermissions(caps) {
		if strings.HasPrefix(p, prefix) {
			matched = append(matched, p)
		}
	}
	if len(matched) == 0 {
		return "the required " + strings.TrimSuffix(prefix, ":") + " permissions"
	}
	return strings.Join(matched, ", ")
}

// isAuthErr reports whether an AWS error is an authorization/permission failure (as opposed to a
// connectivity/config error).
func isAuthErr(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		if strings.Contains(code, "AccessDenied") ||
			strings.Contains(code, "UnauthorizedOperation") ||
			strings.Contains(code, "AuthFailure") ||
			strings.Contains(code, "UnrecognizedClient") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "not authorized")
}

// summarizeErr returns a concise, secret-free description of an AWS error.
func summarizeErr(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("%s: %s", apiErr.ErrorCode(), apiErr.ErrorMessage())
	}
	return err.Error()
}
