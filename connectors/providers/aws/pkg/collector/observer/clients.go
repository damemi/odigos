package observer

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
)

// cachedClients builds AWS clients over the shared per-region config cache, which is where the
// connector's credentials and its assume-role chain already live.
//
// The same two SDK clients the deployer builds, handed back through the READ-ONLY interfaces of this
// package. That is where the read-only guarantee is enforced — *ecs.Client can plainly delete a service,
// and ECSReader cannot express it — so the narrowing here is the whole safety property and not a
// convenience for tests.
type cachedClients struct{ cache *awsclient.Cache }

// NewClients returns the Clients implementation the runtime uses.
func NewClients(cache *awsclient.Cache) Clients { return cachedClients{cache: cache} }

func (c cachedClients) ECS(ctx context.Context, region string) (ECSReader, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return ecs.NewFromConfig(cfg), nil
}

func (c cachedClients) LoadBalancers(ctx context.Context, region string) (ELBReader, error) {
	cfg, err := c.cache.Config(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("resolve aws config for %s: %w", region, err)
	}
	return elb.NewFromConfig(cfg), nil
}
