// Package awsclient holds the shared AWS config/credentials plumbing used by both detection and
// instrumentation: building a per-region aws.Config from the runtime credentials, enumerating the
// account's enabled regions, and a process-lifetime cache of both. Keeping this provider-internal but
// detector/instrumentor-agnostic avoids the detector and instrumentor packages depending on each other.
package awsclient

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/config"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var log = connlog.Subsystem("aws-client")

// DefaultRegion is used for partition-wide calls (region enumeration); always enabled in the
// commercial partition.
const DefaultRegion = "us-east-1"

// BuildConfig resolves an aws.Config for a region from the runtime credentials. Static keys, an
// assumable role (with optional external id), or the ambient default chain are supported.
func BuildConfig(ctx context.Context, region string, creds config.Credentials) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if creds.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, err
	}
	if creds.RoleARN != "" {
		provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), creds.RoleARN, func(o *stscreds.AssumeRoleOptions) {
			if creds.ExternalID != "" {
				o.ExternalID = aws.String(creds.ExternalID)
			}
		})
		cfg.Credentials = aws.NewCredentialsCache(provider)
	}
	return cfg, nil
}

// EnabledRegions returns the regions enabled (opted-in) for the account via ec2:DescribeRegions. The
// connector scans every enabled region — there is no per-connector region selection.
func EnabledRegions(ctx context.Context, cfg aws.Config) ([]string, error) {
	out, err := ec2.NewFromConfig(cfg).DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		log.Error("describe regions failed", "error", err)
		return nil, err
	}
	regions := make([]string, 0, len(out.Regions))
	for _, r := range out.Regions {
		if n := aws.ToString(r.RegionName); n != "" {
			regions = append(regions, n)
		}
	}
	log.Info("enabled regions resolved", "count", len(regions))
	return regions, nil
}

// Cache memoizes the per-region aws.Config and the account's enabled-region list for the process
// lifetime. The configs wrap an auto-refreshing credentials cache, so reusing them across cycles avoids
// repeated AssumeRole calls while still refreshing expiring credentials. Safe for concurrent use and
// shared by detectors and instrumentors.
type Cache struct {
	creds    config.Credentials
	mu       sync.Mutex
	byRegion map[string]aws.Config
	regions  []string
}

// NewCache builds a config cache over the runtime credentials.
func NewCache(creds config.Credentials) *Cache { return &Cache{creds: creds} }

// Config returns (and memoizes) the aws.Config for a region.
func (c *Cache) Config(ctx context.Context, region string) (aws.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cfg, ok := c.byRegion[region]; ok {
		return cfg, nil
	}
	cfg, err := BuildConfig(ctx, region, c.creds)
	if err != nil {
		log.Error("build aws config failed", "region", region, "error", err)
		return aws.Config{}, err
	}
	if c.byRegion == nil {
		c.byRegion = make(map[string]aws.Config)
	}
	c.byRegion[region] = cfg
	log.Debug("aws config cached", "region", region)
	return cfg, nil
}

// VerifyAccount enforces the connector's AWS account boundary: it calls sts:GetCallerIdentity through the
// runtime credentials (after assuming the configured role, if any) and fails unless the resolved account
// equals expectedAccountID. This prevents a misconfigured connector — wrong keys, wrong assumed role, or
// ambient credentials from another account — from discovering or mutating an account other than the one
// it was created for. An empty expectedAccountID is itself a misconfiguration (the boundary cannot be
// enforced) and is rejected.
func (c *Cache) VerifyAccount(ctx context.Context, expectedAccountID string) error {
	if expectedAccountID == "" {
		return fmt.Errorf("no AWS account id configured (ODIGOS_AWS_ACCOUNT_ID); cannot enforce the connector account boundary")
	}
	cfg, err := c.Config(ctx, DefaultRegion)
	if err != nil {
		return fmt.Errorf("resolve aws config for account identity check: %w", err)
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("sts get-caller-identity: %w", err)
	}
	got := aws.ToString(out.Account)
	if got != expectedAccountID {
		return fmt.Errorf("aws account boundary violation: credentials resolve to account %q but the connector is configured for account %q", got, expectedAccountID)
	}
	log.Info("aws account boundary verified", "account", expectedAccountID, "identity", aws.ToString(out.Arn))
	return nil
}

// EnabledRegions resolves (and memoizes) the account's enabled regions via ec2:DescribeRegions.
func (c *Cache) EnabledRegions(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	cached := c.regions
	c.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	base, err := c.Config(ctx, DefaultRegion)
	if err != nil {
		return nil, err
	}
	regions, err := EnabledRegions(ctx, base)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.regions = regions
	c.mu.Unlock()
	return regions, nil
}
