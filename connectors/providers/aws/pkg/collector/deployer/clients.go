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
type cachedClients struct{ cache *awsclient.Cache }

// NewClients returns the Clients implementation the runtime uses.
func NewClients(cache *awsclient.Cache) Clients { return cachedClients{cache: cache} }

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
