package awsprovider

import (
	"context"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcutover"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/types"
)

type cutoverPassKey struct{}

// ReconcilePassContext gives Fargate and Lambda the same pass number. A regional batch is
// partitioned between them, including batch size one, without making observer reads consume it.
func (p *Provider) ReconcilePassContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, cutoverPassKey{}, p.cutoverPass.Add(1))
}

func (p *Provider) collectorBatchQuota(ctx context.Context, kind string, configured int) int {
	pass, ok := ctx.Value(cutoverPassKey{}).(uint64)
	if !ok {
		pass = p.cutoverPass.Load()
	}
	var fargate, lambda bool
	for _, instrumentation := range p.Spec().ActiveInstrumentationTypes(p.cfg.Capabilities) {
		switch instrumentation.Name {
		case types.FargateTask:
			fargate = true
		case types.Lambda:
			lambda = true
		}
	}
	return collectorBatchQuota(configured, kind, fargate && lambda, pass)
}

func collectorBatchQuota(configured int, kind string, bothKinds bool, pass uint64) int {
	if configured <= 0 {
		configured = collectorcutover.DefaultBatchSize
	}
	if !bothKinds {
		return configured
	}
	quota := configured / 2
	if configured%2 == 1 {
		// The spare slot alternates so a batch of one cannot starve either kind.
		if (pass%2 == 0 && kind == "Fargate") || (pass%2 == 1 && kind == "Lambda") {
			quota++
		}
	}
	return quota
}
