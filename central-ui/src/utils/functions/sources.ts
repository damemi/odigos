import { getWorkloadId } from '@odigos/ui-kit/functions';
import type { NamespaceSelectionFormData, SourceSelectionFormData } from '@odigos/ui-kit/store';
import type { NamespaceInstrumentInput, SourceConditions, SourceInstrumentInput, WorkloadWithOdigosHealthStatus } from '@/types';
import {
  EntityTypes,
  DesiredStateProgress,
  StatusType,
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
  podsManifestInjection: null,
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
    // @deprecated-legacy-api(v1.20) - null, not a placeholder. `HealthCard` renders
    // whenever this is truthy, so an `Unknown`-with-no-reason placeholder produced a
    // blank banner. `addAgentInjectionStatusToSources` fills in the real value when
    // the workloads row carries one. Remove when min supported proxy >= v1.20.
    workloadOdigosHealthStatus: s.workloadOdigosHealthStatus ?? null,
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
      // @deprecated-legacy-api(v1.20) - null, not a placeholder. Legacy agents don't
      // report auto-rollback state at all, and an `Unknown` placeholder made
      // StatusesCard render a "Rollback" card for every source plus a Rollback button
      // (its legacy branch fires whenever status !== Success) — an action button on
      // invented data. Remove when min supported proxy >= v1.20.
      autoRollbackStatus: null,
    },
  };
};

// @deprecated-legacy-api(v1.20) - Reverse mapping from the ui-kit's flat
// `StatusType` back to the GraphQL `DesiredStateProgress` enum.
// Used by the legacy condition enrichment path; remove once legacy paths are gone.
function statusTypeToDesiredStateProgress(status: StatusType): DesiredStateProgress {
  switch (status) {
    case StatusType.Error:
      return DesiredStateProgress.Error;
    case StatusType.Warning:
      return DesiredStateProgress.Notice;
    case StatusType.Loading:
      return DesiredStateProgress.Pending;
    case StatusType.Disabled:
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
// @deprecated-legacy-api(v1.20) - legacy condition type -> modern
// `K8sWorkloadConditions` field. This used to be a lower-first heuristic
// (`AgentEnabled` -> `agentEnabled`), which silently dropped every condition whose
// modern field was RENAMED rather than just recased. On a v1.17 agent that meant 4
// of the 5 reported conditions vanished — only `RuntimeDetection` happened to match
// — so the drawer had almost no lifecycle detail to show. Explicit table instead:
// an unmapped name is dropped deliberately, not by accident.
// Remove when min supported proxy >= v1.20.
const LEGACY_CONDITION_TYPE_TO_FIELD: Record<string, keyof K8sWorkloadConditions> = {
  RuntimeDetection: 'runtimeDetection',
  AgentEnabled: 'agentInjectionEnabled',
  WorkloadRollout: 'rollout',
  InstrumentationInstancesHealth: 'processesAgentHealth',
  // Already recase-compatible, listed so the mapping is readable in one place.
  AgentInjected: 'agentInjected',
  PodsManifestInjection: 'podsManifestInjection',
  ExpectingTelemetry: 'expectingTelemetry',
  AgentInjectionEnabled: 'agentInjectionEnabled',
  ProcessesAgentHealth: 'processesAgentHealth',
  Rollout: 'rollout',
  // NOTE: `MarkedForInstrumentation` has no slot in K8sWorkloadConditions — it is a
  // top-level `Workload.markedForInstrumentation` field and is already surfaced as
  // the "Enabled" chip on the drawer's main card, so it is intentionally not mapped.
};

function mergeConditionsIntoWorkloadConditions(base: K8sWorkloadConditions, conditions: Condition[]): K8sWorkloadConditions {
  const result = { ...base };

  for (const c of conditions) {
    const rawKey = c.name || c.type;
    if (!rawKey) continue;

    const fieldName = LEGACY_CONDITION_TYPE_TO_FIELD[rawKey];
    if (!fieldName) continue;

    result[fieldName] = {
      // The legacy wire has no `name` (the PascalCase identity lives on `type`), and
      // the kit keys React elements off `name` — without this the rows render blank
      // and collide. See buildBadgeForDesiredStatus / mapDesiredStatusesToConditions.
      name: c.name || c.type,
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
      // @deprecated-legacy-api(v1.20) - pass through the real status when the
      // workloads row reports one; otherwise null rather than a fabricated
      // placeholder (see mapLegacySourceToWorkload).
      autoRollbackStatus: autoRollback?.autoRollbackStatus ?? null,
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
// @deprecated-legacy-api(v1.20) - `instrumentationInstanceComponents` -> the
// modern `instrumentations` shape. Shared by the two consumers below (processes
// on the Pods tab, containers on the Libraries tab) so they can never drift.
// Remove when min supported proxy >= v1.20.
const mapLibrariesToInstrumentations = (libraries: InstrumentationInstanceComponent[]) =>
  libraries.map((lib) => ({ name: lib.name, healthy: lib.healthy ?? null, message: lib.message ?? null }));

// @deprecated-legacy-api(v1.20) - `describeSource` reports health as a free-form
// `EntityProperty.status` string ('success' | 'error' | ...) rather than the modern
// `DesiredConditionStatus`. Translate rather than invent: the value is the proxy's
// OWN assessment, so we pass its verdict through instead of re-deriving one.
//
// This is deliberately not inferred from `started`/`ready`: on a v1.17 proxy those
// report "false" for containers whose pod `phase` is Running with status 'success'
// (verified live on default/Deployment/coupon), so trusting them would paint a
// healthy pod red — which is exactly the bug this replaces.
// Remove when min supported proxy >= v1.20.
// `name` is REQUIRED, not cosmetic: the kit derives a React key from it
// (`desired-status-${name}` in buildBadgeForDesiredStatus), so two statuses on the
// same row without one collide into duplicate keys. Values mirror the names the
// v1.28+ API actually sends for the same fields — see LEGACY_STATUS_NAMES.
const statusFromEntityProperty = (name: string, prop?: EntityProperty | null): DesiredConditionStatus | null => {
  if (!prop) return null;
  const isSuccess = prop.status?.toLowerCase() === 'success';
  return {
    name,
    status: isSuccess ? DesiredStateProgress.Success : DesiredStateProgress.Error,
    // `explain` is the proxy's human-readable rationale; fall back to the raw value
    // (e.g. "Running") so a tooltip is never blank.
    message: prop.explain || prop.value || '',
  };
};

// @deprecated-legacy-api(v1.20) - condition names the v1.28+ API returns for these
// exact fields, captured from a live agent so the legacy path is key-compatible and
// visually identical. Remove when min supported proxy >= v1.20.
const LEGACY_STATUS_NAMES = {
  podAgentInjection: 'PodAgentInjection',
  podHealthK8s: 'PodHealthK8s',
  podHealthOdigos: 'PodHealthOdigos',
  podContainerHealthOdigos: 'PodContainerHealthOdigos',
} as const;

const mapDescribePodsToWorkloadPods = (describe: Pick<DescribeSource, 'pods'> | null | undefined, libraries: InstrumentationInstanceComponent[]): K8sWorkloadPod[] => {
  const pods = describe?.pods;
  if (!pods?.length) return [];

  const instrumentations = mapLibrariesToInstrumentations(libraries);

  return pods.map((pod) => {
    // @deprecated-legacy-api(v1.20) - odigos health for the pod. Reported
    // instrumentation instances are DIRECT evidence and outrank `agentInjected`,
    // which is a proxy-side heuristic that reads false for eBPF-based agents:
    // on v1.17 a healthy Go pod reports phase Running/success and an instance
    // "Successfully loaded eBPF probes" while `agentInjected` is false/error
    // (verified live on default/Deployment/membership). Trusting `agentInjected`
    // there marks a demonstrably-working pod unhealthy.
    // Only when no instance is reported at all do we fall back to `agentInjected`.
    // Remove when min supported proxy >= v1.20.
    const podInstances = (pod.containers ?? []).flatMap((c) => c.instrumentationInstances ?? []);
    const unhealthyInstance = podInstances.find((i) => !boolFromProp(i.healthy));
    const agentInjectedStatus = statusFromEntityProperty(LEGACY_STATUS_NAMES.podAgentInjection, pod.agentInjected);
    const odigosHealthStatus: DesiredConditionStatus | null = podInstances.length
      ? unhealthyInstance
        ? { name: LEGACY_STATUS_NAMES.podHealthOdigos, status: DesiredStateProgress.Error, message: unhealthyInstance.message?.value ?? '' }
        : { name: LEGACY_STATUS_NAMES.podHealthOdigos, status: DesiredStateProgress.Success, message: podInstances[0]?.message?.value ?? '' }
      : agentInjectedStatus && { ...agentInjectedStatus, name: LEGACY_STATUS_NAMES.podHealthOdigos };

    return {
      podName: pod.podName?.value ?? '',
      nodeName: pod.nodeName?.value ?? '',
      // `PodAnalyze` carries no pod start time on legacy proxies, so Age has nothing
      // real to show. Left empty rather than fabricated.
      startTime: '',
      agentInjected: boolFromProp(pod.agentInjected),
      agentInjectedStatus,
      // Kubernetes health = the proxy's verdict on `phase` ('Running'/'success').
      k8sHealthStatus: statusFromEntityProperty(LEGACY_STATUS_NAMES.podHealthK8s, pod.phase),
      odigosHealthStatus,
      containers: (pod.containers ?? []).map((container) => ({
        containerName: container.containerName?.value ?? '',
        otelDistroName: (container.actualDevices?.value || null) as OtelDistroName | null,
        started: boolFromProp(container.started),
        ready: boolFromProp(container.ready),
        // Deliberately no container-level `k8sHealthStatus`: the only inputs legacy
        // describe offers are `started`/`ready`, which are unreliable here (see
        // `statusFromEntityProperty`). The kit renders no badge when it's absent,
        // which beats a wrong one.
        odigosHealthStatus: (() => {
          const instances = container.instrumentationInstances ?? [];
          if (!instances.length) return null;
          const bad = instances.find((i) => !boolFromProp(i.healthy));
          return bad
            ? { name: LEGACY_STATUS_NAMES.podContainerHealthOdigos, status: DesiredStateProgress.Error, message: bad.message?.value ?? '' }
            : { name: LEGACY_STATUS_NAMES.podContainerHealthOdigos, status: DesiredStateProgress.Success, message: '' };
        })(),
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
    };
  });
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
  const libraries = env?.instrumentationInstanceComponents ?? [];
  const pods = mapDescribePodsToWorkloadPods(env?.describeSource, libraries);
  if (pods.length) {
    // @deprecated-legacy-api(v1.20) - `podsOdigosHealthStatus` gates the whole pods
    // header (`if (!pods?.length || !podsOdigosHealthStatus || !status) return null`)
    // AND supplies its status icon/message. Legacy proxies don't report it, and the
    // `Unknown`-with-empty-message placeholder rendered a blank info card above the
    // counters. Aggregate the per-pod verdicts we just derived instead.
    // Remove when min supported proxy >= v1.20.
    const unhealthyPods = pods.filter((p) => p.odigosHealthStatus?.status === DesiredStateProgress.Error);
    const withStatus = pods.filter((p) => !!p.odigosHealthStatus);
    const podsOdigosHealthStatus: DesiredConditionStatus = unhealthyPods.length
      ? {
          name: LEGACY_STATUS_NAMES.podHealthOdigos,
          status: DesiredStateProgress.Error,
          message: `${unhealthyPods.length} of ${pods.length} pod${pods.length === 1 ? '' : 's'} unhealthy`,
        }
      : withStatus.length
        ? { name: LEGACY_STATUS_NAMES.podHealthOdigos, status: DesiredStateProgress.Success, message: `${withStatus.length} of ${pods.length} pod${pods.length === 1 ? '' : 's'} healthy` }
        : legacyPlaceholderStatus;
    workload = { ...workload, pods, podsOdigosHealthStatus };
  }

  // @deprecated-legacy-api(v1.20) - the same libraries also have to land on
  // `containers[].instrumentations`, because the drawer reads them from two
  // different places: the Pods tab renders
  // `pods[].containers[].processes[].instrumentations` (attached above), while the
  // Libraries tab renders `containers[].instrumentations`. On v1.28+ one query
  // populates both; on legacy proxies only the pod tree was filled, so libraries
  // showed under Pods but the Libraries tab read "No libraries found" for a source
  // the proxy had reported libraries for.
  //
  // `describeSource` exposes libraries as one flat per-source list with no
  // container breakdown, so — exactly as for processes above — broadcast the list
  // onto every container rather than inventing an attribution.
  // Remove when min supported proxy >= v1.20.
  if (libraries.length && workload.containers?.length) {
    const instrumentations = mapLibrariesToInstrumentations(libraries);
    workload = { ...workload, containers: workload.containers.map((c) => ({ ...c, instrumentations })) };
  }

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
