import type { CreateSourceInput, DeleteSourceInput, GetSourceQuery, GetSourcesQuery, GetWorkloadReportsQuery, UpdateSourceInput } from '@odigos/vmagent-api-apollo-client';
import type { GetNamespacesWithWorkloadsData, GetWorkloadsData, OperationContext, PersistSourcesVars, UpdateSourceVars } from '@odigos/ui-kit/contexts';
import type { K8sResourceKind, Workload } from '@odigos/ui-kit/types';
import {
  configBranchForKind,
  lookupSourceConfig,
  lookupWorkloadRegion,
  rememberSourceConfig,
  rememberWorkloadRegion,
  supportsTypedContainerDetails,
  withFieldSelections,
  type VmContainerOverride,
  type VmSourceConfig,
} from '@/graphql/remote/utils';
import { dialectVersion } from '@/graphql/remote/vm-dialect';
import { mergeVmWorkloadReports, withWorkloadReportDetails, type VmWorkloadReport } from '@/graphql/remote/vm-workload-reports';

export type { VmWorkloadReport };
export { buildVmSourcesWithReportsQuery, mergeVmWorkloadReports, withWorkloadReportDetails, withWorkloadReportRegion } from '@/graphql/remote/vm-workload-reports';

// The VM (agents-api) schema is flat and not namespaced: a source is just
// `{ name, kind, serviceName, enabled, config }` with per-source mutations.
// central-ui's domain model is namespaced + bulk, so these helpers translate
// between the two surfaces (resolved from the version-pinned dialect documents).

// VM sources have no namespace; the store keys workloads by `WorkloadId`, so we
// pin them to a single synthetic namespace + data stream.
export const VM_NAMESPACE = 'default';
export const VM_DATA_STREAM = 'default';

export type VmSource = GetSourcesQuery['sources'][number];

// `Source.config` on the wire, widened with the v0.18.0 kind-specific branches the
// pinned dialect package (v0.17.0) does not know about yet. Exactly one branch is
// ever populated, and it must agree with the source's kind.
type VmSourceConfigBranches = {
  ecs?: { containerOverrides?: (VmContainerOverride | null)[] | null } | null;
  docker?: { containerOverrides?: (VmContainerOverride | null)[] | null } | null;
};

/**
 * Flatten whichever kind-specific branch carries content. Reading both in order is
 * unambiguous because the server rejects a config that sets more than one.
 */
const readContainerOverrides = (config: VmSource['config']): VmContainerOverride[] => {
  const branches = config as (VmSource['config'] & VmSourceConfigBranches) | null | undefined;
  const rows = branches?.ecs?.containerOverrides ?? branches?.docker?.containerOverrides ?? [];
  return rows.filter((row): row is VmContainerOverride => !!row?.containerName);
};

const mapVmSourceConfig = (s: VmSource, region: string | null | undefined): VmSourceConfig | null => {
  const config = s.config;
  const runtime = config?.runtimeOverride;
  const distro = config?.distroOverride;
  const containerOverrides = readContainerOverrides(config);

  const runtimeOverride = {
    language: runtime?.language ?? null,
    version: runtime?.version ?? null,
  };

  const distroOverride = {
    otelDistroName: distro?.otelDistroName ?? null,
  };

  const hasRuntime = !!(runtime?.language || runtime?.version);
  const hasDistro = !!distro?.otelDistroName;

  // Remember the config as read — including when nothing is configured, so that a
  // cleared override stays cleared instead of being replayed from a stale entry.
  // The update path rebuilds the whole config from this (see the side-channel in
  // utils.ts), so this has to run even when the mapping below returns null. An
  // unset slot is remembered as `null`, matching what a write that clears it sends.
  rememberSourceConfig(s.kind, s.name, region, {
    runtimeOverride: hasRuntime ? runtimeOverride : null,
    distroOverride: hasDistro ? distroOverride : null,
    containerOverrides,
  });

  if (!hasRuntime && !hasDistro && !containerOverrides.length) return null;

  return { runtimeOverride, distroOverride, ...(containerOverrides.length ? { containerOverrides } : {}) };
};

// agents-api `Source` -> ui-kit `Workload`. Most `Workload` fields are k8s-only
// runtime status the VM surface doesn't carry, so they stay null/omitted; the
// sources table renders off `id`, `serviceName`, `markedForInstrumentation`.
// Callers that also have WorkloadReports should pass them through
// `mergeVmWorkloadReports` (or use `mapVmSourcesWithReportsToWorkloads`).
export const mapVmSourceToWorkload = (s: VmSource): Workload => {
  const region = (s as { region?: string | null }).region;
  // The kit drops region from the persist payload; remember it so a source
  // disabled from this list can still be identified (see utils.ts).
  rememberWorkloadRegion(s.kind, s.name, region);
  return {
    id: {
      namespace: VM_NAMESPACE,
      name: s.name,
      kind: (s.kind as K8sResourceKind) ?? '',
      ...(region ? { region } : {}),
    },
    serviceName: s.serviceName ?? null,
    markedForInstrumentation: { markedForInstrumentation: s.enabled },
    dataStreamNames: [VM_DATA_STREAM],
    numberOfInstances: null,
    autoRollback: { rollbackOccurred: false, autoRollbackStatus: null },
    sourceConfig: mapVmSourceConfig(s, region),
  };
};

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
    // The kit drops region from the persist payload; remember it here so
    // `buildVmPersistSources` can put it back (see the region side-channel in utils.ts).
    rememberWorkloadRegion(kind, report.serviceName, report.region);
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

// `Source.config.{ecs,docker}` (agents-api v0.18.0+). Both branches are requested
// because the response only ever populates the one matching the source's kind, and
// the read path is kind-agnostic. `disabled` is selected so an update can replay a
// container exclusion the UI itself never sets.
const SOURCE_CONFIG_BRANCH_SELECTIONS = `
  ecs {
    containerOverrides {
      containerName
      runtimeOverride { language version }
      distroOverride { otelDistroName }
      disabled
    }
  }
  docker {
    containerOverrides {
      containerName
      runtimeOverride { language version }
      distroOverride { otelDistroName }
      disabled
    }
  }
`;

/**
 * Patch the kind-specific config branches into a printed source document when the
 * connected platform's schema has them.
 *
 * Every source read has to be patched, not just the drawer's: the update path
 * rebuilds `Source.config` from whatever the last read remembered, so a read that
 * skipped the branches would leave the side-channel believing the source has no
 * container overrides and the next update would delete them.
 */
export const patchVmSourceQuery = (printedQuery: string, ctx: OperationContext): string => {
  if (!supportsTypedContainerDetails(dialectVersion(ctx))) return printedQuery;
  return withFieldSelections(printedQuery, 'config', SOURCE_CONFIG_BRANCH_SELECTIONS);
};

/** `patchQuery` for the combined sources + workloadReports reads. */
export const patchVmSourcesWithReportsQuery = (printedQuery: string, ctx: OperationContext): string => withWorkloadReportDetails(patchVmSourceQuery(printedQuery, ctx), ctx);

/**
 * Apply a runtime/distro patch to one container's entry, preserving the rest.
 *
 * The patch is authoritative for BOTH override slots, matching the workload-level
 * contract below — the kit's leaf forms emit only the field they edited and inject
 * the sibling before calling the API. `disabled` is not something the kit can
 * express, so it is carried over from the existing entry rather than dropped, and
 * an entry left with nothing to say is removed instead of sent as an empty row.
 */
const upsertContainerOverride = (existing: VmContainerOverride[], containerName: string, runtimeOverride: VmContainerOverride['runtimeOverride'], distroOverride: VmContainerOverride['distroOverride']): VmContainerOverride[] => {
  const previous = existing.find((o) => o.containerName === containerName);
  const others = existing.filter((o) => o.containerName !== containerName);
  const disabled = previous?.disabled ?? undefined;

  if (!runtimeOverride && !distroOverride && !disabled) return others;

  return [...others, { containerName, runtimeOverride: runtimeOverride ?? null, distroOverride: distroOverride ?? null, ...(disabled ? { disabled } : {}) }];
};

// `UPDATE_SOURCE` vars -> agents-api `updateSource(input: UpdateSourceInput!)`.
//
// agents-api REPLACES the whole Source.config when `config` is set, so everything
// the customer configured has to be present in every write:
//   - the kit sends both workload-level override slots together (its leaf forms
//     inject the sibling), so `null` / empty there means "clear this override";
//   - the per-container layer is invisible to the kit, so it is replayed from the
//     side-channel in utils.ts. Without that, editing the workload-level runtime
//     would silently delete every per-container override on the source.
//
// A patch carrying `containerName` is scoped to that container (the kit's
// Containers card), so it lands in the kind-specific branch and leaves the
// workload-level slots exactly as they were.
export const vmUpdateSourceVariables = (vars: UpdateSourceVars, ctx?: OperationContext): Record<string, unknown> => {
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

    const runtimeOverride = hasRuntimeOverride
      ? {
          language: language ?? undefined,
          version: typeof version === 'string' && version.trim() !== '' ? version : undefined,
        }
      : null;
    const distroOverride = hasDistroOverride ? { otelDistroName } : null;

    // Only a kind whose branch the server accepts can carry a container layer, and
    // only a schema that defines the branch can be sent one.
    const branch = ctx && supportsTypedContainerDetails(dialectVersion(ctx)) ? configBranchForKind(sourceId.kind) : undefined;
    const region = sourceId.region ?? lookupWorkloadRegion(sourceId.kind, sourceId.name);
    const stored = branch ? lookupSourceConfig(sourceId.kind, sourceId.name, region) : undefined;
    const containerName = branch ? patch.containerName?.trim() : undefined;

    const containerOverrides = containerName ? upsertContainerOverride(stored?.containerOverrides ?? [], containerName, runtimeOverride, distroOverride) : (stored?.containerOverrides ?? []);

    input.config = {
      // A container-scoped patch says nothing about the workload-level slots, so
      // they are replayed rather than rebuilt from the patch.
      ...(containerName ? { runtimeOverride: stored?.runtimeOverride ?? null, distroOverride: stored?.distroOverride ?? null } : { runtimeOverride, distroOverride }),
      // An empty list has to be omitted, not sent as `{ containerOverrides: [] }`:
      // the server counts a branch as set only when it carries content, and an
      // empty branch from a generated client is an artifact, not a choice.
      ...(branch && containerOverrides.length ? { [branch]: { containerOverrides } } : {}),
    } as UpdateSourceInput['config'];
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
    const kind = s.kind || '';
    // `s.region` is always undefined in practice — the kit's payload has no such
    // field — so fall back to the region remembered when the workload was
    // discovered. Kept as a first choice in case a future kit version carries it.
    const region = (s as typeof s & { region?: string }).region ?? lookupWorkloadRegion(kind || s.namespace, s.name);
    if (s.selected) {
      varDefs.push(`$${key}: CreateSourceInput!`);
      selections.push(`s${i}: createSource(input: $${key}) { name }`);
      variables[key] = {
        name: s.name,
        kind,
        enabled: true,
        ...(region ? { region } : {}),
      } as CreateSourceInput;
    } else {
      varDefs.push(`$${key}: DeleteSourceInput!`);
      selections.push(`s${i}: deleteSource(input: $${key}) { name deleted }`);
      variables[key] = {
        name: s.name,
        ...(s.kind ? { kind: s.kind } : {}),
        ...(region ? { region } : {}),
      } as DeleteSourceInput;
    }
  });

  // An empty bulk persist still needs a valid mutation document.
  if (!selections.length) return { query: 'mutation PersistVmSources { __typename }', variables: {} };

  const query = `mutation PersistVmSources(${varDefs.join(', ')}) {\n  ${selections.join('\n  ')}\n}`;
  return { query, variables };
};
