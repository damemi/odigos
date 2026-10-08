package deployer

import (
	"context"
	"fmt"

	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
)

// cachedClients builds AWS clients over the shared per-region config cache, which is where the
// connector's credentials and its assume-role chain already live.
type cachedClients struct{ cache awsclient.ConfigSource }

// NewClients returns the Clients implementation the runtime uses.
func NewClients(cache awsclient.ConfigSource) Clients { return cachedClients{cache: cache} }

func (c cachedClients) Logs(ctx context.Context, region string) (LogsClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return cloudwatchlogs.NewFromConfig(cfg), nil
}

func (c cachedClients) Secrets(ctx context.Context, region string) (SecretsClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return secretsmanager.NewFromConfig(cfg), nil
}

func (c cachedClients) EC2(ctx context.Context, region string) (SecurityGroupClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return ec2.NewFromConfig(cfg), nil
}

func (c cachedClients) LoadBalancers(ctx context.Context, region string) (LoadBalancerClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return elb.NewFromConfig(cfg), nil
}

func (c cachedClients) ECS(ctx context.Context, region string) (ECSClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return ecs.NewFromConfig(cfg), nil
}

// AutoScaling is regional, like the ECS service it governs: a scalable target is registered in the
// region its resource id names, and one built for the wrong region reports the target as absent rather
// than failing — which would read as "nothing registered" and register a second one.
func (c cachedClients) AutoScaling(ctx context.Context, region string) (AutoScalingClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return aas.NewFromConfig(cfg), nil
}

// destructiveClients builds the AWS clients teardown uses.
//
// A SECOND type over the same cache rather than more methods on cachedClients, and it has to be: the
// two interfaces name the same seven services, so one type cannot carry both — `ECS` would have to
// return an ECSClient and an ECSTeardownClient at once. The compiler enforcing that is a better
// guarantee than a comment asking nobody to add a delete to the converge path.
type destructiveClients struct{ cache awsclient.ConfigSource }

// NewTeardownClients returns the TeardownClients implementation the runtime uses.
func NewTeardownClients(cache awsclient.ConfigSource) TeardownClients {
	return destructiveClients{cache: cache}
}

func (c destructiveClients) Logs(ctx context.Context, region string) (LogGroupTeardownClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return cloudwatchlogs.NewFromConfig(cfg), nil
}

func (c destructiveClients) Secrets(ctx context.Context, region string) (SecretTeardownClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return secretsmanager.NewFromConfig(cfg), nil
}

func (c destructiveClients) EC2(ctx context.Context, region string) (SecurityGroupTeardownClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return ec2.NewFromConfig(cfg), nil
}

func (c destructiveClients) LoadBalancers(ctx context.Context, region string) (LoadBalancerTeardownClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return elb.NewFromConfig(cfg), nil
}

func (c destructiveClients) ECS(ctx context.Context, region string) (ECSTeardownClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return ecs.NewFromConfig(cfg), nil
}

func (c destructiveClients) AutoScaling(ctx context.Context, region string) (ScalingTeardownClient, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return aas.NewFromConfig(cfg), nil
}
