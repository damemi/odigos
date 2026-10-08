package deployer

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
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
