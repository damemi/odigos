package loop

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"

	"github.com/odigos-io/odigos/insights/internal/interrogation/db/clickhouse"
)

// instrumentationConfigForSource loads the InstrumentationConfig for the source
// workload. The object name is "<kind>-<name>" in the workload namespace
// (for example deployment-checkout). A missing object returns a NotFound error.
func instrumentationConfigForSource(ctx context.Context, c client.Client, source clickhouse.SourceKey) (*odigosv1.InstrumentationConfig, error) {
	if c == nil {
		return nil, nil
	}
	name := workload.CalculateWorkloadRuntimeObjectName(source.WorkloadName, source.WorkloadKind)
	var ic odigosv1.InstrumentationConfig
	err := c.Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: name}, &ic)
	if err != nil {
		return nil, fmt.Errorf("get instrumentation config %s/%s: %w", source.Namespace, name, err)
	}
	return &ic, nil
}
