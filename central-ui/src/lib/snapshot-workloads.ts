import type { GetWorkloadsData, GetWorkloadsVars } from '@odigos/ui-kit/contexts';
import { DesiredStateProgress, K8sResourceKind, ProgrammingLanguages, type Workload } from '@odigos/ui-kit/types';

/** Snapshot workload fields from central-backend (includes PLAT-1302 enrichments). */
export interface SnapshotWorkload {
  namespace: string;
  name: string;
  kind: string;
  selected: boolean;
  otelServiceName?: string | null;
  healthStatus?: string | null;
  dataStreamNames?: string[] | null;
  detectedLanguages?: string[] | null;
  agentInjectionStatus?: string | null;
}

export interface SnapshotNamespace {
  name: string;
  selected?: boolean | null;
  workloads: SnapshotWorkload[];
}

export interface SnapshotCluster {
  clusterId: string;
  clusterName: string;
  namespaces: SnapshotNamespace[];
  timestamp?: number;
}

const SNAPSHOT_OVERLAY_TTL_MS = 30_000;

type SnapshotOverlay = {
  snapshot: SnapshotCluster;
  minTimestamp: number;
  expiresAt: number;
};

/** In-memory overlays so declarative useApiQuery can prefer a live fallback over stale Redis. */
const workloadSnapshotOverlays = new Map<string, SnapshotOverlay>();

const toProgress = (value?: string | null): DesiredStateProgress | undefined => {
  if (!value) return undefined;
  if (Object.values(DesiredStateProgress).includes(value as DesiredStateProgress)) {
    return value as DesiredStateProgress;
  }
  return DesiredStateProgress.Unknown;
};

const toLanguages = (langs?: string[] | null): ProgrammingLanguages[] => {
  if (!langs?.length) return [];
  return langs.filter((l): l is ProgrammingLanguages => Object.values(ProgrammingLanguages).includes(l as ProgrammingLanguages));
};

const workloadKey = (namespace: string, kind: string, name: string) => `${namespace}/${kind}/${name}`;

const mapWorkloadSnapshot = (wl: SnapshotWorkload): Workload => {
  const health = toProgress(wl.healthStatus);
  const agentInjection = toProgress(wl.agentInjectionStatus);
  const detectedLanguages = toLanguages(wl.detectedLanguages);

  return {
    id: {
      namespace: wl.namespace,
      kind: wl.kind as K8sResourceKind,
      name: wl.name,
    },
    serviceName: wl.otelServiceName ?? null,
    dataStreamNames: wl.dataStreamNames ?? [],
    markedForInstrumentation: { markedForInstrumentation: wl.selected },
    runtimeInfo: detectedLanguages.length ? { detectedLanguages } : null,
    workloadOdigosHealthStatus: health
      ? {
          name: 'WorkloadOdigosHealthStatus',
          status: health,
          reasonEnum: null,
          message: '',
        }
      : null,
    podsAgentInjectionStatus: agentInjection
      ? {
          name: 'PodsAgentInjectionStatus',
          status: agentInjection,
          reasonEnum: null,
          message: '',
        }
      : null,
  };
};

/**
 * Map a Redis-backed cluster snapshot into the kit's GetWorkloads list shape.
 * Prefer this over RemoteFetch for modern k8s proxies (avoids multi-MB GraphQL
 * responses over the proxy WebSocket).
 */
export const mapClusterSnapshotToWorkloads = (snapshot: SnapshotCluster | null | undefined, vars?: GetWorkloadsVars): GetWorkloadsData => {
  const markedFilter = vars?.filter?.markedForInstrumentation;
  const workloads: Workload[] = [];

  for (const ns of snapshot?.namespaces ?? []) {
    for (const wl of ns.workloads ?? []) {
      if (markedFilter === true && !wl.selected) continue;
      if (markedFilter === false && wl.selected) continue;
      workloads.push(mapWorkloadSnapshot(wl));
    }
  }

  return { workloads };
};

/** True when any workload in the snapshot is marked selected / instrumented. */
export const snapshotHasSelectedWorkloads = (snapshot: SnapshotCluster | null | undefined): boolean => {
  for (const ns of snapshot?.namespaces ?? []) {
    for (const wl of ns.workloads ?? []) {
      if (wl.selected) return true;
    }
  }
  return false;
};

/**
 * Build a Redis-shaped clusterSnapshot from live RemoteFetch workloads.
 * Used only when Redis has no base snapshot yet — prefer mergeLiveSelectedIntoSnapshot
 * so AddSourceDrawer still sees the full workload inventory.
 */
export const workloadsToClusterSnapshot = (clusterId: string, clusterName: string, workloads: Workload[]): SnapshotCluster => {
  const byNs = new Map<string, SnapshotWorkload[]>();

  for (const w of workloads) {
    const namespace = w.id.namespace;
    const list = byNs.get(namespace) ?? [];
    list.push({
      namespace,
      name: w.id.name,
      kind: String(w.id.kind),
      selected: !!w.markedForInstrumentation?.markedForInstrumentation,
      otelServiceName: w.serviceName ?? null,
      healthStatus: w.workloadOdigosHealthStatus?.status ?? null,
      dataStreamNames: w.dataStreamNames ?? [],
      detectedLanguages: w.runtimeInfo?.detectedLanguages ?? [],
      agentInjectionStatus: w.podsAgentInjectionStatus?.status ?? null,
    });
    byNs.set(namespace, list);
  }

  return {
    clusterId,
    clusterName,
    timestamp: Date.now(),
    namespaces: Array.from(byNs.entries()).map(([name, nsWorkloads]) => ({
      name,
      selected: nsWorkloads.some((w) => w.selected),
      workloads: nsWorkloads,
    })),
  };
};

/**
 * Merge live RemoteFetch selected-workloads into a full Redis snapshot so we can
 * writeQuery GET_CLUSTER_SNAPSHOT without stripping unselected workloads that
 * AddSourceDrawer still needs.
 */
export const mergeLiveSelectedIntoSnapshot = (
  base: SnapshotCluster | null | undefined,
  clusterId: string,
  clusterName: string,
  live: Workload[],
): SnapshotCluster => {
  if (!base?.namespaces?.length) {
    return workloadsToClusterSnapshot(clusterId, clusterName, live);
  }

  const liveByKey = new Map(live.map((w) => [workloadKey(w.id.namespace, String(w.id.kind), w.id.name), w]));
  const seen = new Set<string>();

  const namespaces: SnapshotNamespace[] = base.namespaces.map((ns) => {
    const workloads = (ns.workloads ?? []).map((wl) => {
      const key = workloadKey(wl.namespace, wl.kind, wl.name);
      const liveWl = liveByKey.get(key);
      if (!liveWl) return wl;
      seen.add(key);
      return {
        ...wl,
        selected: !!liveWl.markedForInstrumentation?.markedForInstrumentation,
        otelServiceName: liveWl.serviceName ?? wl.otelServiceName,
        healthStatus: liveWl.workloadOdigosHealthStatus?.status ?? wl.healthStatus,
        dataStreamNames: liveWl.dataStreamNames ?? wl.dataStreamNames,
        detectedLanguages: liveWl.runtimeInfo?.detectedLanguages ?? wl.detectedLanguages,
        agentInjectionStatus: liveWl.podsAgentInjectionStatus?.status ?? wl.agentInjectionStatus,
      };
    });

    // Append live workloads missing from this namespace in the base snapshot.
    // Use Array.from — project TS target doesn't allow for-of over Map.
    Array.from(liveByKey.entries()).forEach(([key, liveWl]) => {
      if (seen.has(key) || liveWl.id.namespace !== ns.name) return;
      seen.add(key);
      workloads.push({
        namespace: liveWl.id.namespace,
        name: liveWl.id.name,
        kind: String(liveWl.id.kind),
        selected: !!liveWl.markedForInstrumentation?.markedForInstrumentation,
        otelServiceName: liveWl.serviceName ?? null,
        healthStatus: liveWl.workloadOdigosHealthStatus?.status ?? null,
        dataStreamNames: liveWl.dataStreamNames ?? [],
        detectedLanguages: liveWl.runtimeInfo?.detectedLanguages ?? [],
        agentInjectionStatus: liveWl.podsAgentInjectionStatus?.status ?? null,
      });
    });

    return {
      ...ns,
      selected: workloads.some((w) => w.selected) || !!ns.selected,
      workloads,
    };
  });

  // Live workloads in namespaces Redis doesn't have yet.
  const missingByNs = new Map<string, SnapshotWorkload[]>();
  Array.from(liveByKey.entries()).forEach(([key, liveWl]) => {
    if (seen.has(key)) return;
    const list = missingByNs.get(liveWl.id.namespace) ?? [];
    list.push({
      namespace: liveWl.id.namespace,
      name: liveWl.id.name,
      kind: String(liveWl.id.kind),
      selected: !!liveWl.markedForInstrumentation?.markedForInstrumentation,
      otelServiceName: liveWl.serviceName ?? null,
      healthStatus: liveWl.workloadOdigosHealthStatus?.status ?? null,
      dataStreamNames: liveWl.dataStreamNames ?? [],
      detectedLanguages: liveWl.runtimeInfo?.detectedLanguages ?? [],
      agentInjectionStatus: liveWl.podsAgentInjectionStatus?.status ?? null,
    });
    missingByNs.set(liveWl.id.namespace, list);
  });
  Array.from(missingByNs.entries()).forEach(([name, workloads]) => {
    namespaces.push({ name, selected: workloads.some((w) => w.selected), workloads });
  });

  return {
    clusterId: base.clusterId || clusterId,
    clusterName: base.clusterName || clusterName,
    timestamp: Date.now(),
    namespaces,
  };
};

export const setWorkloadSnapshotOverlay = (clusterId: string, snapshot: SnapshotCluster) => {
  const ts = snapshot.timestamp ?? Date.now();
  workloadSnapshotOverlays.set(clusterId, {
    snapshot: { ...snapshot, timestamp: ts },
    minTimestamp: ts,
    expiresAt: Date.now() + SNAPSHOT_OVERLAY_TTL_MS,
  });
};

/**
 * Prefer an in-memory overlay when Redis is still missing selected workloads
 * (or is older than the overlay). Clears the overlay once Redis catches up.
 */
export const resolveClusterSnapshot = (clusterId: string | undefined, redis: SnapshotCluster | null | undefined): SnapshotCluster | null | undefined => {
  if (!clusterId) return redis;
  const overlay = workloadSnapshotOverlays.get(clusterId);
  if (!overlay) return redis;

  if (Date.now() > overlay.expiresAt) {
    workloadSnapshotOverlays.delete(clusterId);
    return redis;
  }

  const redisTs = redis?.timestamp ?? 0;
  const overlayHasSelected = snapshotHasSelectedWorkloads(overlay.snapshot);
  const redisHasSelected = snapshotHasSelectedWorkloads(redis);

  // Redis caught up to (or past) the overlay and reflects selection state.
  if (redisTs >= overlay.minTimestamp && (!overlayHasSelected || redisHasSelected)) {
    workloadSnapshotOverlays.delete(clusterId);
    return redis;
  }

  if (overlayHasSelected && !redisHasSelected) return overlay.snapshot;
  if ((overlay.snapshot.timestamp ?? 0) > redisTs) return overlay.snapshot;
  return redis ?? overlay.snapshot;
};
