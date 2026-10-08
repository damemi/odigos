package awsclient

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// ConfigSource is the small AWS configuration surface used by Collector clients.
type ConfigSource interface {
	Config(context.Context, string) (aws.Config, error)
}

// CollectorRoleCache assumes the customer-provisioned role for Collector reads,
// convergence, and teardown. The connector's own credentials are used only to
// obtain temporary role credentials. Collector placement scans configured regions.
type CollectorRoleCache struct {
	base      *Cache
	accountID string
	roleFor   func(string) string
	mu        sync.Mutex
	byRole    map[string]aws.Config
}

func NewCollectorRoleCache(base *Cache, accountID string, roleFor func(string) string) *CollectorRoleCache {
	return &CollectorRoleCache{base: base, accountID: accountID, roleFor: roleFor}
}

func (c *CollectorRoleCache) Config(ctx context.Context, region string) (aws.Config, error) {
	role := strings.TrimSpace(c.roleFor(region))
	if err := validateCollectorRole(role, c.accountID); err != nil {
		return aws.Config{}, fmt.Errorf("collector in %s: %w", region, err)
	}
	key := region + "\x00" + role
	c.mu.Lock()
	defer c.mu.Unlock()
	if cfg, ok := c.byRole[key]; ok {
		return cfg, nil
	}
	base, err := c.base.Config(ctx, region)
	if err != nil {
		return aws.Config{}, err
	}
	provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(base), role, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = "odigos-collector"
	})
	base.Credentials = aws.NewCredentialsCache(provider)
	// Fail before any infrastructure call if the trust relationship or the
	// connector's ability to assume this role is missing.
	if _, err := base.Credentials.Retrieve(ctx); err != nil {
		return aws.Config{}, fmt.Errorf("assume customer Collector deployment role %s: %w", role, err)
	}
	if c.byRole == nil {
		c.byRole = make(map[string]aws.Config)
	}
	c.byRole[key] = base
	return base, nil
}

func validateCollectorRole(ref, accountID string) error {
	parsed, err := arn.Parse(ref)
	if err != nil || parsed.Service != "iam" || parsed.AccountID != accountID || !strings.HasPrefix(parsed.Resource, "role/") || len(parsed.Resource) <= len("role/") {
		return fmt.Errorf("deploymentRoleRef must be an IAM role ARN in AWS account %s", accountID)
	}
	return nil
}
