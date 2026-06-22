import { getWorkloadId } from '@odigos/ui-kit/functions';
import type { NamespaceSelectionFormData, SourceSelectionFormData } from '@odigos/ui-kit/store';
import type { NamespaceInstrumentInput, SourceConditions, SourceInstrumentInput, WorkloadWithOdigosHealthStatus } from '@/types';
import {
  EntityTypes,
  DesiredStateProgress,
  StatusType,
  OtherStatus,
  type WorkloadId,
  type Workload,
  type Condition,
  type DesiredConditionStatus,
  type K8sWorkloadConditions,
  type K8sWorkloadPod,
  type InstrumentationInstanceComponent,
  type DescribeSource,
  type EntityProperty,
  type ProgrammingLanguages,
  type OtelDistroName,
  type Namespace,
  type K8sResourceKind,
} from '@odigos/ui-kit/types';

// @deprecated-legacy-api(v1.20) - the GraphQL `K8sActualSource` shape used by
// proxies < v1.20 returns container fields flat (language, runtimeVersion,
// instrumented, instrumentationMessage, otelDistroName, overriden). Lift them
// into the nested ui-kit `Workload`/`K8sWorkloadContainer` shape so downstream
// code can be shape-agnostic. Remove this when min supported proxy >= v1.20.
export interface LegacySourceContainer {
  containerName: string;
  language: ProgrammingLanguages;
  runtimeVersion: string;
  overriden?: boolean;
  instrumented: boolean;
  instrumentationMessage: string;
  otelDistroName?: OtelDistroName | null;
}

// @deprecated-legacy-api(v1.20) - matches the flat `K8sActualSource` GraphQL
// shape returned by proxies < v1.20.
export interface LegacySource extends WorkloadId {
  selected?: boolean;
  otelServiceName?: string;
  numberOfInstances?: number | null;
  dataStreamNames?: string[] | null;
  containers: LegacySourceContainer[] | null;
  conditions?: Condition[] | null;
  workloadOdigosHealthStatus?: DesiredConditionStatus | null;
  detectedLanguages?: ProgrammingLanguages[] | null;
  podsAgentInjectionStatus?: DesiredConditionStatus | null;
  podsHealthStatus?: DesiredConditionStatus | null;
  rollbackOccurred?: boolean;
}

// Placeholder used for required Workload statuses that legacy sources do not provide.
// The legacy enrichment helpers (addConditionToSources / addAgentInjectionStatusToSources)
// fill these in via follow-up queries when available.
const legacyPlaceholderStatus: DesiredConditionStatus = {
  status: DesiredStateProgress.Unknown,
  message: '',
};

const emptyWorkloadConditions = (): K8sWorkloadConditions => ({
  runtimeDetection: null,
  agentInjectionEnabled: null,
  rollout: null,
  agentInjected: null,
  processesAgentHealth: null,
  expectingTelemetry: null,
});

// @deprecated-legacy-api(v1.20) - Lift the flat `K8sActualSource` shape into
// the nested ui-kit `Workload` shape so the store can stay homogeneous.
// Required Workload fields that legacy sources don't carry are filled with
// placeholders; the legacy enrichment helpers below patch the real values in.
export const mapLegacySourceToWorkload = (s: LegacySource): Workload => {
  return {
    id: { namespace: s.namespace, kind: s.kind, name: s.name },
    serviceName: s.otelServiceName ?? null,
    workloadOdigosHealthStatus: s.workloadOdigosHealthStatus ?? legacyPlaceholderStatus,
    conditions: s.conditions?.length ? mergeConditionsIntoWorkloadConditions(emptyWorkloadConditions(), s.conditions) : emptyWorkloadConditions(),
    markedForInstrumentation: { markedForInstrumentation: null },
    runtimeInfo: s.detectedLanguages ? { detectedLanguages: s.detectedLanguages } : null,
    containers:
      s.containers?.map((c) => ({
        containerName: c.containerName,
        runtimeInfo: { language: c.language, runtimeVersion: c.runtimeVersion },
        agentEnabled: {
          agentEnabled: c.instrumented,
          agentEnabledStatus: { ...legacyPlaceholderStatus, message: c.instrumentationMessage },
          otelDistroName: c.otelDistroName ?? null,
        },
        overrides: c.overriden ? { containerName: c.containerName, runtimeInfo: { language: c.language, runtimeVersion: c.runtimeVersion }, otelDistroName: c.otelDistroName ?? null } : null,
      })) ?? null,
    pods: null,
    podsAgentInjectionStatus: s.podsAgentInjectionStatus ?? legacyPlaceholderStatus,
    podsOdigosHealthStatus: s.podsHealthStatus ?? legacyPlaceholderStatus,
    telemetryMetrics: [],
    dataStreamNames: s.dataStreamNames ?? [],
    numberOfInstances: s.numberOfInstances,
    autoRollback: {
      rollbackOccurred: s.rollbackOccurred ?? false,
      autoRollbackStatus: { ...legacyPlaceholderStatus },
    },
  };
};

// @deprecated-legacy-api(v1.20) - Reverse mapping from the ui-kit's flat
// `StatusType`/`OtherStatus` back to the GraphQL `DesiredStateProgress` enum.
// Used by the legacy condition enrichment path; remove once legacy paths are gone.
function statusTypeToDesiredStateProgress(status: StatusType | OtherStatus): DesiredStateProgress {
  switch (status) {
    case StatusType.Error:
      return DesiredStateProgress.Error;
    case StatusType.Warning:
      return DesiredStateProgress.Notice;
    case OtherStatus.Loading:
      return DesiredStateProgress.Pending;
    case OtherStatus.Disabled:
      return DesiredStateProgress.Disabled;
    case StatusType.Success:
      return DesiredStateProgress.Success;
    case StatusType.Info:
    default:
      return DesiredStateProgress.Unknown;
  }
}

// @deprecated-legacy-api(v1.20) - Merges a list of flat `Condition` entries
// (from the legacy `GET_SOURCE_CONDITIONS` query) into the nested
// `K8sWorkloadConditions` shape that `Workload.conditions` now uses.
// Conditions whose name doesn't match a known field are dropped.
function mergeConditionsIntoWorkloadConditions(base: K8sWorkloadConditions, conditions: Condition[]): K8sWorkloadConditions {
  const result = { ...base };
  const knownFields = new Set<keyof K8sWorkloadConditions>(['runtimeDetection', 'agentInjectionEnabled', 'rollout', 'agentInjected', 'processesAgentHealth', 'expectingTelemetry']);

  for (const c of conditions) {
    const rawKey = c.name || c.type;
    if (!rawKey) continue;

    const fieldName = (rawKey.charAt(0).toLowerCase() + rawKey.slice(1)) as keyof K8sWorkloadConditions;
    if (!knownFields.has(fieldName)) continue;

    result[fieldName] = {
      name: c.name,
      status: statusTypeToDesiredStateProgress(c.status),
      reasonEnum: c.reason,
      message: c.message ?? '',
    };
  }

  return result;
}

// @deprecated-legacy-api(v1.20) - Remove addConditionToSources and
// addAgentInjectionStatusToSources when min supported proxy >= v1.20.
// Also remove SourceConditions and WorkloadWithOdigosHealthStatus from the import above.
export const addConditionToSources = ({ namespace, name, kind, conditions }: SourceConditions, sources: Workload[]): Workload | null => {
  const foundIdx = sources.findIndex((x) => x.id.namespace === namespace && x.id.name === name && x.id.kind === kind);
  if (foundIdx === -1) return null;

  return {
    ...sources[foundIdx],
    conditions: mergeConditionsIntoWorkloadConditions(sources[foundIdx].conditions ?? emptyWorkloadConditions(), conditions),
  };
};

export const addAgentInjectionStatusToSources = (
  { id: { namespace, name, kind }, podsAgentInjectionStatus, workloadOdigosHealthStatus, autoRollback }: WorkloadWithOdigosHealthStatus,
  sources: Workload[],
): Workload | null => {
  const foundIdx = sources.findIndex((x) => x.id.namespace === namespace && x.id.name === name && x.id.kind === kind);
  if (foundIdx === -1) return null;

  const updatedSource: Workload = {
    ...sources[foundIdx],
    podsAgentInjectionStatus,
    autoRollback: {
      rollbackOccurred: autoRollback?.rollbackOccurred ?? sources[foundIdx].autoRollback?.rollbackOccurred ?? false,
      autoRollbackStatus: { ...legacyPlaceholderStatus },
    },
  };

  if (workloadOdigosHealthStatus) {
    updatedSource.workloadOdigosHealthStatus = workloadOdigosHealthStatus;
  }

  return updatedSource;
};

// @deprecated-legacy-api(v1.20) - decoded envelope of the combined
// `GET_SOURCE_WITH_DETAILS` query (proxies < v1.20). Each piece mirrors one of
// the old standalone queries the central-ui used to fire separately:
//   - `computePlatform.source`            -> GET_SOURCE_BY_ID (flat K8sActualSource)
//   - `sourceConditions`                  -> GET_SOURCE_CONDITIONS
//   - `instrumentationInstanceComponents` -> GET_SOURCE_LIBRARIES
//   - `workloads`                         -> the agent-injection / health rows
export interface CombinedLegacySourceEnvelope {
  computePlatform?: { source?: LegacySource | null } | null;
  sourceConditions?: SourceConditions[] | null;
  instrumentationInstanceComponents?: InstrumentationInstanceComponent[] | null;
  // Only the `pods` tree is selected by the combined query; type as a partial.
  describeSource?: Pick<DescribeSource, 'pods'> | null;
  workloads?: WorkloadWithOdigosHealthStatus[] | null;
}

// @deprecated-legacy-api(v1.20) - the describe `EntityProperty` carries its
// value as a string; the workload booleans we map into are plain booleans.
const boolFromProp = (prop?: EntityProperty | null): boolean => prop?.value?.toLowerCase() === 'true';

// @deprecated-legacy-api(v1.20) - the redesigned (`_v2`) source drawer surfaces
// pods (and their per-process instrumentation libraries) under
// `pods[].containers[].processes[].instrumentations[]` — on v1.20+ this comes
// straight from `workloadsByIds`. Legacy proxies don't return pods on the
// workload query, but the old `describeSource` query does, so we map its live
// pod tree into the modern `Workload.pods` shape (real pod / container / process
// data, no fabrication). The flat `instrumentationInstanceComponents` (detected
// libraries, which describe doesn't break down per-process) are attached to each
// mapped process so the drawer's "Instrumentation Libraries" tab stays populated.
const mapDescribePodsToWorkloadPods = (describe: Pick<DescribeSource, 'pods'> | null | undefined, libraries: InstrumentationInstanceComponent[]): K8sWorkloadPod[] => {
  const pods = describe?.pods;
  if (!pods?.length) return [];

  const instrumentations = libraries.map((lib) => ({ name: lib.name, healthy: lib.healthy ?? null, message: lib.message ?? null }));

  return pods.map((pod) => ({
    podName: pod.podName?.value ?? '',
    nodeName: pod.nodeName?.value ?? '',
    startTime: '',
    agentInjected: boolFromProp(pod.agentInjected),
    containers: (pod.containers ?? []).map((container) => ({
      containerName: container.containerName?.value ?? '',
      otelDistroName: (container.actualDevices?.value || null) as OtelDistroName | null,
      started: boolFromProp(container.started),
      ready: boolFromProp(container.ready),
      processes: (container.instrumentationInstances ?? []).map((instance) => {
        const healthy = boolFromProp(instance.healthy);
        return {
          healthy,
          healthStatus: {
            status: healthy ? DesiredStateProgress.Success : DesiredStateProgress.Error,
            message: instance.message?.value ?? '',
          },
          identifyingAttributes: (instance.identifyingAttributes ?? []).map((attr) => ({ name: attr.name, value: attr.value })),
          // describe gives no per-process library breakdown on legacy proxies, so
          // surface the source's detected libraries on every process (best effort).
          instrumentations: instrumentations.length ? instrumentations : null,
        };
      }),
    })),
  }));
};

// @deprecated-legacy-api(v1.20) - map the combined `GET_SOURCE_WITH_DETAILS`
// envelope into a single v1.20+ `Workload` (the shape produced by
// `GET_WORKLOADS_BY_IDS` / GET_WORKLOAD_BY_ID) so the source drawer renders the
// same data on legacy proxies as it does on modern ones:
//   1. flat source           -> base Workload (containers + inline conditions)
//   2. sourceConditions       -> merged workload-level conditions
//   3. workloads injection    -> agent-injection + odigos health status
//   4. describeSource pods     -> real pod / container / process tree
//   5. instrumentation libs    -> instrumentations attached to mapped processes
// Remove when min supported proxy >= v1.20.
export const mapCombinedLegacySourceToWorkload = (env: CombinedLegacySourceEnvelope | null | undefined): Workload | undefined => {
  const source = env?.computePlatform?.source;
  if (!source) return undefined;

  let workload = mapLegacySourceToWorkload(source);

  // Merge the additional workload-level conditions (rollout / agent-injection-
  // enabled / ...) keyed by namespace/kind/name onto the base source conditions.
  const matchedConditions = env?.sourceConditions?.find((c) => c.namespace === source.namespace && c.name === source.name && c.kind === source.kind);
  if (matchedConditions) {
    workload = addConditionToSources(matchedConditions, [workload]) ?? workload;
  }

  // Merge the agent-injection / odigos-health status from the workloads rows.
  const matchedInjection = env?.workloads?.find((w) => w.id.namespace === source.namespace && w.id.name === source.name && w.id.kind === source.kind);
  if (matchedInjection) {
    workload = addAgentInjectionStatusToSources(matchedInjection, [workload]) ?? workload;
  }

  // Surface real pods (K8s only; VM has no `describeSource`) from the describe
  // query, attaching the detected instrumentation libraries to each process.
  const pods = mapDescribePodsToWorkloadPods(env?.describeSource, env?.instrumentationInstanceComponents ?? []);
  if (pods.length) workload = { ...workload, pods };

  return workload;
};

// @deprecated-legacy-api(v1.20) - legacy `K8sActualSource` shape returned
// nested under `K8sActualNamespace.sources` by proxies < v1.20. Uses
// `selected` instead of `markedForInstrumentation`.
export interface LegacyK8sActualSource {
  namespace: string;
  kind: string;
  name: string;
  dataStreamNames: string[];
  selected: boolean;
  numberOfInstances: number | null;
}

// @deprecated-legacy-api(v1.20) - legacy `K8sActualNamespace` shape returned
// by proxies < v1.20. Uses `selected` instead of `markedForInstrumentation`
// and a flat `sources` field instead of the v1.20+ nested `workloads`.
export interface LegacyK8sActualNamespace {
  name: string;
  selected: boolean;
  dataStreamNames: string[];
  sources?: LegacyK8sActualSource[];
}

// @deprecated-legacy-api(v1.20) - converts the legacy `K8sActualNamespace`
// shape into the v1.20+ `Namespace` shape that the ui-kit now consumes.
// Remove when min supported proxy >= v1.20.
export const legacyNamespaceToNamespace = (ns: LegacyK8sActualNamespace): Namespace => {
  return {
    name: ns.name,
    markedForInstrumentation: ns.selected,
    dataStreamNames: ns.dataStreamNames,
    workloads: ns.sources?.map((s) => ({
      id: { namespace: s.namespace, kind: s.kind as K8sResourceKind | '', name: s.name },
      markedForInstrumentation: { markedForInstrumentation: s.selected },
      dataStreamNames: s.dataStreamNames,
      numberOfInstances: s.numberOfInstances,
    })),
  };
};

export const prepareSourcePayloads = (
  selectAppsList: SourceSelectionFormData,
  existingSources: Workload[],
  selectedStreamName: string,
  handleInstrumentationCount: (toAddCount: number, toDeleteCount: number) => void,
  removeEntities: (entityType: EntityTypes, entityIds: WorkloadId[]) => void,
  addEntities: (entityType: EntityTypes, entities: Workload[]) => void,
) => {
  let isEmpty = true;
  const payload: SourceInstrumentInput = { sources: [] };

  for (const [ns, items] of Object.entries(selectAppsList)) {
    if (items.length) {
      isEmpty = false;

      const mappedItems = items.map(({ id, selected, currentStreamName }) => ({
        namespace: id.namespace,
        name: id.name,
        kind: id.kind,
        // this is to map selected=undefined to selected=false
        selected: selected === undefined ? false : selected,
        // currentStreamName comes from the UI Kit, if it's missing we use selectedStreamName as a fallback,
        // we could rely on only the selectedStreamName, but if we want to override the selected then we need to use the currentStreamName
        // (for example - if we want to have a single page to manage all groups, then we need to override the selected)
        currentStreamName: currentStreamName || selectedStreamName,
      }));

      const toAddToStore: Workload[] = [];
      const toUpdateInStore: Workload[] = [];
      const toDeleteFromStore: WorkloadId[] = [];

      let toAddCount = 0;
      let toDeleteCount = 0;

      for (const item of mappedItems) {
        const foundExisting = existingSources.find((src) => src.id.namespace === ns && src.id.name === item.name && src.id.kind === item.kind);

        // Check if the instrumenting-source does not exist, this confirms an expected creation of the CRD
        if (item.selected && !foundExisting) {
          toAddCount++;
          toAddToStore.push({ ...getWorkloadId(item), dataStreamNames: [selectedStreamName] });
        }
        // Else the instrumenting-source should be updated in store to include the selected stream name
        else if (item.selected && foundExisting) {
          toUpdateInStore.push({ ...foundExisting, dataStreamNames: (foundExisting.dataStreamNames || []).concat([selectedStreamName]) });
        }

        // Check if the uninstrumenting-source has 1 or none data streams, this confirms an expected deletion of the CRD
        else if (!item.selected && foundExisting && (foundExisting.dataStreamNames || []).length <= 1) {
          toDeleteCount++;
          toDeleteFromStore.push(getWorkloadId(foundExisting));
        }
        // Else the uninstrumenting-source should be updated in store to exclude the selected stream name
        else if (!item.selected && foundExisting) {
          toUpdateInStore.push({ ...foundExisting, dataStreamNames: (foundExisting.dataStreamNames || []).filter((name) => name !== selectedStreamName) });
        }
      }

      handleInstrumentationCount(toAddCount, toDeleteCount);
      addEntities(EntityTypes.Source, toAddToStore);
      addEntities(EntityTypes.Source, toUpdateInStore);
      removeEntities(EntityTypes.Source, toDeleteFromStore);

      payload.sources.push(...mappedItems);
    }
  }

  return { payload, isEmpty };
};

export const prepareNamespacePayloads = (futureSelectAppsList: NamespaceSelectionFormData, selectedStreamName: string): { payload: NamespaceInstrumentInput; isEmpty: boolean } => {
  let isEmpty = true;
  const payload: NamespaceInstrumentInput = { namespaces: [] };

  for (const [ns, { selected, currentStreamName }] of Object.entries(futureSelectAppsList)) {
    if (typeof selected === 'boolean') {
      isEmpty = false;
      payload.namespaces.push({
        namespace: ns,
        selected: selected,

        // currentStreamName comes from the UI Kit, if it's missing we use selectedStreamName as a fallback,
        // we could rely on only the selectedStreamName, but if we want to override the selected then we need to use the currentStreamName
        // (for example - if we want to have a single page to manage all groups, then we need to override the selected)
        currentStreamName: currentStreamName || selectedStreamName,
      });
    }
  }

  return { payload, isEmpty };
};
