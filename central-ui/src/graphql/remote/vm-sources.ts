import type { CreateSourceInput, DeleteSourceInput, GetSourceQuery, GetSourcesQuery, GetWorkloadReportsQuery, UpdateSourceInput } from '@odigos/vmagent-api-apollo-client';
import type { GetNamespacesWithWorkloadsData, GetWorkloadsData, PersistSourcesVars, UpdateSourceVars } from '@odigos/ui-kit/contexts';
import type { K8sResourceKind, SourceConfig, Workload } from '@odigos/ui-kit/types';
import { mergeVmWorkloadReports, type VmWorkloadReport } from '@/graphql/remote/vm-workload-reports';

export type { VmWorkloadReport };
export { buildVmSourcesWithReportsQuery, mergeVmWorkloadReports, withWorkloadReportRegion } from '@/graphql/remote/vm-workload-reports';

// The VM (agents-api) schema is flat and not namespaced: a source is just
// `{ name, kind, serviceName, enabled, config }` with per-source mutations.
// central-ui's domain model is namespaced + bulk, so these helpers translate
// between the two surfaces (resolved from the version-pinned dialect documents).

// VM sources have no namespace; the store keys workloads by `WorkloadId`, so we
// pin them to a single synthetic namespace + data stream.
export const VM_NAMESPACE = 'default';
export const VM_DATA_STREAM = 'default';

export type VmSource = GetSourcesQuery['sources'][number];

const mapVmSourceConfig = (config: VmSource['config']): SourceConfig | null => {
  if (!config) return null;
  const runtime = config.runtimeOverride;
  const distro = config.distroOverride;
  const hasRuntime = !!(runtime?.language || runtime?.version);
  const hasDistro = !!distro?.otelDistroName;
  if (!hasRuntime && !hasDistro) return null;

  const runtimeOverride = {
    language: runtime?.language ?? null,
    version: runtime?.version ?? null,
  };

  const distroOverride = {
    otelDistroName: distro?.otelDistroName ?? null,
  };

  return { runtimeOverride, distroOverride };
};

// agents-api `Source` -> ui-kit `Workload`. Most `Workload` fields are k8s-only
// runtime status the VM surface doesn't carry, so they stay null/omitted; the
// sources table renders off `id`, `serviceName`, `markedForInstrumentation`.
// Callers that also have WorkloadReports should pass them through
// `mergeVmWorkloadReports` (or use `mapVmSourcesWithReportsToWorkloads`).
export const mapVmSourceToWorkload = (s: VmSource): Workload => ({
  id: {
    namespace: VM_NAMESPACE,
    name: s.name,
    kind: (s.kind as K8sResourceKind) ?? '',
    ...((s as { region?: string | null }).region ? { region: (s as { region?: string | null }).region } : {}),
  },
  serviceName: s.serviceName ?? null,
  markedForInstrumentation: { markedForInstrumentation: s.enabled },
  dataStreamNames: [VM_DATA_STREAM],
  numberOfInstances: null,
  autoRollback: { rollbackOccurred: false, autoRollbackStatus: null },
  sourceConfig: mapVmSourceConfig(s.config),
});

// `GetSources` envelope (`{ sources: [...] }`) -> `GET_WORKLOADS` result.
export const mapVmSourcesToWorkloads = (data: unknown): GetWorkloadsData => {
  const sources = (data as GetSourcesQuery | null | undefined)?.sources ?? [];
  return { workloads: sources.map(mapVmSourceToWorkload) };
};

/** Combined `{ sources, workloadReports }` envelope → enriched Workloads. */
export const mapVmSourcesWithReportsToWorkloads = (data: unknown): GetWorkloadsData => {
  const envelope = data as { sources?: VmSource[]; workloadReports?: VmWorkloadReport[] } | null | undefined;
  const workloads = (envelope?.sources ?? []).map(mapVmSourceToWorkload);
  return { workloads: mergeVmWorkloadReports(workloads, envelope?.workloadReports ?? []) };
};

// `GetSource` envelope (`{ source: {...} }`) -> `GET_SOURCE` result.
export const mapVmSourceResult = (data: unknown): { source?: Workload } => {
  const source = (data as GetSourceQuery | null | undefined)?.source;
  return { source: source ? mapVmSourceToWorkload(source) : undefined };
};

// The "Add Sources" drawer is a discovery view, so it lists discoverable
// workloads (`workloadReports`), not already-configured sources. VM has no
// namespaces; the kit relabels the left column "Kinds", so we group the flat
// report list by `kind` (one synthetic namespace per kind, e.g. docker/systemd/
// process). A report is "selected" when a matching source already exists
// (`desired.sourceMatched`). Persist then keys `createSource`/`deleteSource`
// off `name` (= the report's `serviceName`) + `kind` (+ `region` for cloud
// connectors, where the same name can exist in multiple regions).
export const mapVmWorkloadReportsToNamespaces = (data: unknown): GetNamespacesWithWorkloadsData => {
  const reports = ((data as GetWorkloadReportsQuery | null | undefined)?.workloadReports ?? []) as VmWorkloadReport[];

  const byKind = new Map<string, VmWorkloadReport[]>();
  for (const report of reports) {
    const kind = report.kind ?? '';
    const bucket = byKind.get(kind);
    if (bucket) bucket.push(report);
    else byKind.set(kind, [report]);
  }

  return {
    namespaces: Array.from(byKind.entries()).map(([kind, kindReports]) => ({
      name: kind,
      markedForInstrumentation: false,
      dataStreamNames: [VM_DATA_STREAM],
      workloads: kindReports.map((report) => ({
        id: {
          namespace: kind,
          name: report.serviceName,
          kind: (kind as K8sResourceKind) ?? '',
          ...(report.region ? { region: report.region } : {}),
        },
        markedForInstrumentation: { markedForInstrumentation: !!report.desired?.sourceMatched },
        dataStreamNames: [VM_DATA_STREAM],
        numberOfInstances: null,
      })),
    })),
  };
};

// `UPDATE_SOURCE` vars -> agents-api `updateSource(input: UpdateSourceInput!)`.
// agents-api replaces the whole Source.config when `config` is set, so the kit
// must send both override slots together. `null` / empty clears an override.
export const vmUpdateSourceVariables = (vars: UpdateSourceVars): Record<string, unknown> => {
  const { sourceId, patchSourceRequest } = vars;
  const input: UpdateSourceInput = { name: sourceId.name };
  if (sourceId.kind) input.kind = sourceId.kind;
  if (sourceId.region) (input as UpdateSourceInput & { region?: string }).region = sourceId.region;
  if (patchSourceRequest?.otelServiceName !== undefined) input.serviceName = patchSourceRequest.otelServiceName;

  const patch = patchSourceRequest ?? {};
  const touchesRuntime = 'language' in patch || 'version' in patch;
  const touchesDistro = 'otelDistroName' in patch;

  if (touchesRuntime || touchesDistro) {
    const language = patch.language;
    const version = patch.version;
    const otelDistroName = patch.otelDistroName;

    const hasRuntimeOverride = language != null || (typeof version === 'string' && version.trim() !== '');
    const hasDistroOverride = otelDistroName != null && otelDistroName !== '';

    input.config = {
      runtimeOverride: hasRuntimeOverride
        ? {
            language: language ?? undefined,
            version: typeof version === 'string' && version.trim() !== '' ? version : undefined,
          }
        : null,
      distroOverride: hasDistroOverride ? { otelDistroName } : null,
    };
  }

  return { input };
};

// `PERSIST_SOURCES` is a single bulk mutation in central-ui, but agents-api only
// exposes per-source `createSource`/`deleteSource`. There's no single VM
// document for it, so we fan out into one combined, aliased mutation (a single
// REMOTE_FETCH round-trip) instead: enabling a source creates it, disabling it
// deletes it. The query string and the variables map are built from the same
// ordered `sources` list so their aliases/`$inputN` line up.
//
// Cloud connectors require `region` on create/delete (source identity is
// name+kind+region); VM agents leave it unset and the connector/VM ignores it.
export const buildVmPersistSources = (vars: PersistSourcesVars): { query: string; variables: Record<string, unknown> } => {
  const sources = vars?.sources ?? [];
  const varDefs: string[] = [];
  const selections: string[] = [];
  const variables: Record<string, unknown> = {};

  sources.forEach((s, i) => {
    const key = `input${i}`;
    if (s.selected) {
      varDefs.push(`$${key}: CreateSourceInput!`);
      selections.push(`s${i}: createSource(input: $${key}) { name }`);
      variables[key] = {
        name: s.name,
        kind: s.kind || '',
        enabled: true,
        ...(s.region ? { region: s.region } : {}),
      } as CreateSourceInput;
    } else {
      varDefs.push(`$${key}: DeleteSourceInput!`);
      selections.push(`s${i}: deleteSource(input: $${key}) { name deleted }`);
      variables[key] = {
        name: s.name,
        ...(s.kind ? { kind: s.kind } : {}),
        ...(s.region ? { region: s.region } : {}),
      } as DeleteSourceInput;
    }
  });

  // An empty bulk persist still needs a valid mutation document.
  if (!selections.length) return { query: 'mutation PersistVmSources { __typename }', variables: {} };

  const query = `mutation PersistVmSources(${varDefs.join(', ')}) {\n  ${selections.join('\n  ')}\n}`;
  return { query, variables };
};
