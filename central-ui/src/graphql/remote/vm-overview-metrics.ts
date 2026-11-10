import type { GetMetricsData } from '@odigos/ui-kit/contexts';
import type { K8sResourceKind } from '@odigos/ui-kit/types';
import { VM_NAMESPACE } from '@/graphql/remote/vm-sources';

type VmOverviewMetricsEnvelope = {
  overviewMetrics?: {
    sources?: { name: string; kind: string; region?: string | null; throughput: number }[] | null;
    destinations?: { name: string; throughput: number }[] | null;
  } | null;
};

/**
 * Agents-api `overviewMetrics` (v0.37.0) → kit `GET_METRICS` slot. Sources are keyed
 * like `mapVmSourceToWorkload` (synthetic VM namespace + name + kind, plus region for
 * cloud connectors) so the kit's id match finds them; destinations by name, the VM
 * destination id.
 */
export const mapVmOverviewMetrics = (data: unknown): GetMetricsData | undefined => {
  const metrics = (data as VmOverviewMetricsEnvelope | null | undefined)?.overviewMetrics;
  if (!metrics) return undefined;
  return {
    getOverviewMetrics: {
      sources: (metrics.sources ?? []).map((s) => ({
        namespace: VM_NAMESPACE,
        name: s.name,
        kind: s.kind as K8sResourceKind,
        ...(s.region ? { region: s.region } : {}),
        throughput: s.throughput,
      })),
      destinations: (metrics.destinations ?? []).map((d) => ({ id: d.name, throughput: d.throughput })),
    },
  };
};
