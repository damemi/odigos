'use client';

/**
 * `<OdigosApiAdapter>` — wires the kit's `<OdigosApiProvider>` to the
 * central-ui's REMOTE_FETCH transport.
 *
 * Owns:
 *   1. Apollo client config: HTTP + WS link, JWT auth header, redirect on 401.
 *   2. `wrapRemote()` — converts every `VersionedRemoteFetch` constant into
 *      an `Operation` whose document is `REMOTE_FETCH`, with
 *      `transformVariables` resolving the per-version GraphQL string and
 *      `transformResult` parsing the stringified-JSON server response.
 *   3. `wrapRemoteMulti()` — extends `wrapRemote` with `documentMulti` /
 *      `transformVariablesMulti` / `transformMultiResult` so the kit can
 *      fan out across multiple proxies via REMOTE_MULTI_FETCH (grouped by
 *      resolved version).
 *   4. Out-of-proxy operations (DELETE_PROXY_CONNECTION, GET_COMPUTE_PLATFORMS,
 *      GET_ALL_CLUSTER_SNAPSHOTS, etc.) — bare `gql` documents.
 *   5. Operation context derived from the active `useProxy()` proxy id
 *      (platformType + odigosVersion + proxyID) plus `useConfig().isReadonly`.
 *
 * Future flexibility points (kit-supported, not yet used here):
 *   - `Operation.client?` per-op override — set this if a specific
 *     operation should run against a different ApolloClient (e.g. a
 *     pre-built client from `@odigos/vmagent-api-apollo-client` that talks
 *     directly to a vm-agent, bypassing `REMOTE_FETCH`). The kit's runner
 *     will use `op.client ?? defaultClient`.
 *   - `pickByPlatform(ctx, { K8s, Vm })` from
 *     `@odigos/ui-kit/contexts` — drop-in replacement for the
 *     `if (ctx.platformType === PlatformType.Vm) … else …` switches that
 *     today live inside individual `transformVariables` callbacks.
 *   - `versionedDocument(map)` and `vmDialectMap(dialects, select)` — when
 *     migrating off the central-ui-native `VersionedRemoteFetch` strings
 *     toward the vmagent codegen `dialects` registry, use these to feed
 *     printed `DocumentNode`s straight into `Operation.document(ctx)`. The
 *     kit will pick the right dialect bucket per `ctx.schemaVersion ??
 *     ctx.version` using the same floor-to-bucket rule as `getVersionedQuery`.
 */

import React, { type FC, type PropsWithChildren, useCallback, useEffect, useMemo, useState } from 'react';
import { map } from 'rxjs';
import { GraphQLError } from 'graphql';
import { createClient } from 'graphql-ws';
import { useConfig } from '@/hooks/config/useConfig';
import { ErrorLink } from '@apollo/client/link/error';
import { useUserStore, useProxyStore } from '@/store';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { vmQuery, type VmSelect } from '@/graphql/remote/vm-dialect';
import { safeJsonParse, trimVersion } from '@odigos/ui-kit/functions';
import { PlatformType, Tier, type Action } from '@odigos/ui-kit/types';
import { mapVmRuleTypesToOptions } from '@/graphql/remote/vm-rule-types';
import { CombinedGraphQLErrors, ServerError } from '@apollo/client/errors';
import { mapVmActionTypesToOptions } from '@/graphql/remote/vm-action-types';
import { ApolloLink, type ApolloClient, type DocumentNode } from '@apollo/client';
import type { VersionedRemoteFetch, WorkloadWithOdigosHealthStatus } from '@/types';
import { normalizeActionForWire, parseRenamesString } from '@/lib/action-form-normalization';
import {
  mapClusterSnapshotToWorkloads,
  mergeLiveSelectedIntoSnapshot,
  resolveClusterSnapshot,
  setWorkloadSnapshotOverlay,
  snapshotHasSelectedWorkloads,
  type SnapshotCluster,
} from '@/lib/snapshot-workloads';
import {
  buildVmPersistSources,
  buildVmSourcesWithReportsQuery,
  mapVmSourceResult,
  mapVmSourcesToWorkloads,
  mapVmSourcesWithReportsToWorkloads,
  mapVmWorkloadReportsToNamespaces,
  vmUpdateSourceVariables,
  withWorkloadReportRegion,
} from '@/graphql/remote/vm-sources';
import { supportsWorkloadReportRegion } from '@/graphql/remote/utils';
import { mapVmActions, vmCreateActionResult, vmCreateActionVariables, vmDeleteActionResult, vmDeleteActionVariables, vmUpdateActionResult, vmUpdateActionVariables } from '@/graphql/remote/vm-actions';
import {
  mapVmDestinationTypesToCategories,
  mapVmDestinations,
  vmCreateDestinationResult,
  vmCreateDestinationVariables,
  vmDeleteDestinationResult,
  vmDeleteDestinationVariables,
  vmTestConnectionResult,
  vmTestConnectionVariables,
  vmUpdateDestinationResult,
  vmUpdateDestinationVariables,
} from '@/graphql/remote/vm-destinations';
import {
  mapVmInstrumentationRules,
  vmCreateInstrumentationRuleResult,
  vmCreateInstrumentationRuleVariables,
  vmDeleteInstrumentationRuleResult,
  vmDeleteInstrumentationRuleVariables,
  vmUpdateInstrumentationRuleResult,
  vmUpdateInstrumentationRuleVariables,
} from '@/graphql/remote/vm-instrumentation-rules';
import type {
  CreateActionMutation,
  CreateInstrumentationRuleMutation,
  DeleteActionMutation,
  DeleteInstrumentationRuleMutation,
  GetActionsQuery,
  GetInstrumentationRulesQuery,
  UpdateActionMutation,
  UpdateInstrumentationRuleMutation,
} from '@odigos/vmagent-api-apollo-client';
import {
  API,
  ROUTES,
  getVersionedQuery,
  // @deprecated-legacy-api(v1.20) - legacy <v1.20 shape mappers, used only by
  // the `wrapVersioned` legacy branches below. Remove with the legacy paths.
  mapLegacySourceToWorkload,
  mapCombinedLegacySourceToWorkload,
  addAgentInjectionStatusToSources,
  legacyNamespaceToNamespace,
  type LegacySource,
  type LegacyK8sActualNamespace,
  type CombinedLegacySourceEnvelope,
} from '@/utils';
import {
  OdigosApiProvider,
  type CreateActionResult,
  type CreateActionVars,
  type CreateCostReductionRuleResult,
  type CreateDestinationResult,
  type CreateDestinationVars,
  type CreateHighlyRelevantOperationRuleResult,
  type CreateInstrumentationRuleResult,
  type CreateInstrumentationRuleVars,
  type CreateNoisyOperationRuleResult,
  type DeleteActionResult,
  type DeleteActionVars,
  type DeleteCostReductionRuleResult,
  type DeleteDataStreamResult,
  type DeleteDataStreamVars,
  type DeleteDestinationResult,
  type DeleteDestinationVars,
  type DeleteHighlyRelevantOperationRuleResult,
  type DeleteInstrumentationRuleResult,
  type DeleteInstrumentationRuleVars,
  type DeleteNoisyOperationRuleResult,
  type DescribeOdigosData,
  type DescribeSourceData,
  type GetActionsData,
  type GetCollectorPodInfoVars,
  type GetConfigYamlsData,
  type GetDataStreamsData,
  type GetDestinationsData,
  type GetInstrumentationRulesData,
  type GetK8sManifestVars,
  type GetMetricsData,
  type GetNamespacesWithWorkloadsData,
  type GetPeerSourcesVars,
  type GetSamplingRulesData,
  type GetServiceMapData,
  type GetSourceLibrariesData,
  type GetSourceLibrariesVars,
  type GetWorkloadsByIdsData,
  type GetWorkloadsByIdsVars,
  type GetWorkloadsData,
  type GetWorkloadsVars,
  type MultiFetchGroup,
  type MultiFetchResult,
  type OdigosApiOperations,
  type Operation,
  type OperationContext,
  type PersistNamespacesResult,
  type PersistNamespacesVars,
  type PersistSourcesResult,
  type PersistSourcesVars,
  type RecoverFromRollbackVars,
  type ResetLocalUiConfigResult,
  type RestartPodVars,
  type RestartWorkloadsVars,
  type SamplingRuleCreateVars,
  type SamplingRuleDeleteVars,
  type SamplingRuleUpdateVars,
  type TestDestinationConnectionVars,
  type UpdateActionResult,
  type UpdateActionVars,
  type UpdateCostReductionRuleResult,
  type UpdateDataStreamResult,
  type UpdateDataStreamVars,
  type UpdateDestinationData,
  type UpdateDestinationVars,
  type UpdateHighlyRelevantOperationRuleResult,
  type UpdateInstrumentationRuleResult,
  type UpdateInstrumentationRuleVars,
  type UpdateLocalUiConfigResult,
  type UpdateLocalUiConfigVars,
  type UpdateLocalUiSamplingConfigResult,
  type UpdateLocalUiSamplingConfigVars,
  type UpdateNoisyOperationRuleResult,
  type UpdateRemoteConfigResult,
  type UpdateRemoteConfigVars,
  type UpdateSourceVars,
  type UpdateTokenResult,
  type UpdateTokenVars,
} from '@odigos/ui-kit/contexts';
import type {
  AllClusterSnapshots,
  ClusterSnapshot,
  CostReductionRuleInput,
  EffectiveConfig,
  EnableProfilingResult,
  ExtendedPodInfo,
  FetchedConfig,
  GatewayInfo,
  HighlyRelevantOperationRuleInput,
  NodeCollectoInfo,
  NoisyOperationRuleInput,
  PeerSources,
  GetActionTypesResult,
  GetInstrumentationRuleTypesResult,
  GetDestinationCategoriesResult,
  GetPotentialDestinationsResult,
  PodInfo,
  ProfilingSlots,
  SamplingRules,
  SamplingRulesK8sHealthConfig,
  ServiceMapSources,
  SourceProfilingResult,
  TestConnectionResponse,
  TokenPayload,
  Workload,
  WorkloadId,
} from '@odigos/ui-kit/types';
import {
  // central-only direct gql operations
  REMOTE_FETCH,
  REMOTE_MULTI_FETCH,
  GET_COMPUTE_PLATFORMS,
  UPDATE_COMPUTE_PLATFORM,
  DELETE_PROXY_CONNECTION,
  GET_ALL_CLUSTER_SNAPSHOTS,
  GET_CLUSTER_SNAPSHOT,
  GET_HEALTH,
  GET_SYSTEM_CONFIG,
  // versioned (REMOTE_FETCH-wrapped) operations
  GET_ACTIONS,
  GET_ACTION_TYPES,
  GET_CONFIG,
  GET_CONFIG_YAMLS,
  GET_DATA_STREAMS,
  GET_DESCRIBE_ODIGOS,
  GET_DESCRIBE_SOURCE,
  GET_DESTINATIONS,
  GET_DESTINATION_CATEGORIES,
  GET_EFFECTIVE_CONFIG,
  GET_INSTRUMENTATION_RULES,
  GET_INSTRUMENTATION_RULE_TYPES,
  GET_K8S_MANIFEST,
  GET_METRICS,
  GET_NAMESPACES_WITH_WORKLOADS,
  GET_NAMESPACES_WITH_SOURCES,
  GET_GATEWAY_INFO,
  GET_GATEWAY_PODS,
  GET_NODE_COLLECTOR_INFO,
  GET_NODE_COLLECTOR_PODS,
  GET_COLLECTOR_POD_INFO,
  GET_PROFILING_SLOTS,
  GET_SOURCE_PROFILING,
  GET_SAMPLING_RULES,
  GET_SERVICE_MAP,
  GET_SOURCES_WITH_STATUS,
  GET_SOURCE,
  GET_SOURCE_WITH_DETAILS,
  GET_PEER_SOURCES,
  GET_SOURCE_LIBRARIES,
  GET_TOKENS,
  GET_WORKLOADS,
  GET_WORKLOADS_BY_IDS_SLIM,
  GET_WORKLOADS_BY_IDS,
  GET_POTENTIAL_DESTINATIONS,
  // versioned mutations
  CREATE_ACTION,
  UPDATE_ACTION,
  DELETE_ACTION,
  UPDATE_DATA_STREAM,
  DELETE_DATA_STREAM,
  CREATE_DESTINATION,
  UPDATE_DESTINATION,
  DELETE_DESTINATION,
  TEST_DESTINATION_CONNECTION,
  CREATE_INSTRUMENTATION_RULE,
  UPDATE_INSTURMENTATION_RULE,
  DELETE_INSTURMENTATION_RULE,
  UPDATE_LOCAL_UI_CONFIG,
  RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS,
  PERSIST_NAMESPACES,
  ENABLE_SOURCE_PROFILING,
  UPDATE_REMOTE_CONFIG,
  CREATE_NOISY_OPERATION_RULE,
  UPDATE_NOISY_OPERATION_RULE,
  DELETE_NOISY_OPERATION_RULE,
  CREATE_HIGHLY_RELEVANT_OPERATION_RULE,
  UPDATE_HIGHLY_RELEVANT_OPERATION_RULE,
  DELETE_HIGHLY_RELEVANT_OPERATION_RULE,
  CREATE_COST_REDUCTION_RULE,
  UPDATE_COST_REDUCTION_RULE,
  DELETE_COST_REDUCTION_RULE,
  UPDATE_LOCAL_UI_SAMPLING_CONFIG,
  PERSIST_SOURCES,
  UPDATE_SOURCE,
  RESTART_WORKLOADS,
  RESTART_POD,
  RECOVER_FROM_ROLLBACK,
  UPDATE_TOKEN,
} from '@/graphql';

// Suppress unused warnings for ops that aren't yet wired into the kit's
// OdigosApiOperations (kept for future use).
void GET_HEALTH;
void GET_SYSTEM_CONFIG;
void UPDATE_COMPUTE_PLATFORM;

/**
 * Shape of the `REMOTE_FETCH` server response — a single stringified JSON
 * payload that the adapter decodes into the operation's TData.
 */
interface RemoteFetchResponse {
  remoteFetch?: string;
}

/**
 * Shape of the `REMOTE_MULTI_FETCH` server response — one row per proxy,
 * each carrying its own stringified JSON `data` to be decoded into TData.
 */
interface RemoteMultiFetchResponse {
  remoteMultiFetch?: Array<{
    proxyID: string;
    success: boolean;
    data?: string;
    error?: string;
  }>;
}

/** Decoded inner shape of a single proxy's GraphQL response. */
interface RemoteParsedPayload<T> {
  data?: T;
  errors?: { message: string }[];
}

/**
 * Wrap a `VersionedRemoteFetch` into an `Operation` that runs through
 * REMOTE_FETCH (single proxy) or REMOTE_MULTI_FETCH (multi proxy via
 * `connectionIds`).
 *
 * The host adapter resolves the per-version GraphQL string in
 * `transformVariables` so the kit's runner doesn't need to know about
 * versioned-query maps. Result parsing unwraps the stringified JSON
 * `remoteFetch` payload.
 *
 * Generic `<TData, TVars>` lets each `wrapRemote(...)` call site declare
 * the exact server-side response and request shapes for that operation,
 * so the kit's `OdigosApiOperations` slot types align without `any`.
 *
 * The optional `extract` argument is the bridge between the GraphQL
 * envelope (`{ gatewayInfo: …, gatewayPods: …, … }`) and the kit's
 * canonical bare slot shape (`GatewayInfo`, `PodInfo[]`, …). Pass it
 * for any slot whose `TData` differs from the wire shape, so consumers
 * never have to narrow `envelope-or-bare` unions at the call site.
 *   - `extract` is invoked with the GraphQL response's `data` field
 *     (already unwrapped from the stringified `remoteFetch`).
 *   - For multi-fetch, each row's parsed `data` runs through `extract`
 *     too so the per-proxy results are also bare-typed.
 */
/**
 * Resolve the proxy a REMOTE_FETCH op should be addressed to. In-proxy
 * pages set `ctx.proxyID`; connections-scope (bulk drawers /
 * EditConfigDrawer) instead sets `ctx.connectionIds` and we use the first
 * selected connection as the coordinator proxy. Returns undefined when
 * neither is present (truly out-of-proxy) so `canRun` can short-circuit.
 */
const resolveProxyID = (ctx: OperationContext): string | undefined => ctx.proxyID ?? ctx.connectionIds?.[0];

// Local superset of the kit's `OperationContext` that also carries the GraphQL
// dialect/schema version. VM proxies report their agents-api schema version
// separately from the vm-agent binary version, so `ctx.version` stays the
// agent version (what the user sees as "installed") while `schemaVersion`
// drives dialect/bucket selection. The published kit doesn't declare this
// field yet; once it does (it's added to the kit's `OperationContext`), this
// alias collapses to `OperationContext` and the casts below become no-ops.
type ProxyOperationContext = OperationContext & { schemaVersion?: string };

// Version used to pick the GraphQL document/dialect bucket. VM proxies use the
// reported schema version; everything else falls back to the agent version
// (k8s never sets `schemaVersion`, so this equals `ctx.version` there).
const dialectVersion = (ctx: OperationContext): string => (ctx as ProxyOperationContext).schemaVersion ?? ctx.version;

// A cloud connector runtime serves the exact same agents-api GraphQL schema as
// the VM agent (it registers over the same reverse WebSocket — see
// connectors/base/central/client.go), so it resolves documents from the same
// vmagent dialect registry and reuses every `VmSpec`. Treat VM and connector
// uniformly wherever the adapter selects the agents-api dialect over the
// hand-written k8s `VersionedRemoteFetch` buckets.
const isAgentsApiPlatform = (ctx: OperationContext): boolean => ctx.platformType === PlatformType.Vm || ctx.platformType === PlatformType.Connector;

// ---------------------------------------------------------------------------
// Version-aware input sanitizing.
//
// The kit always builds mutation inputs against the LATEST schema (e.g. it
// always includes `exportedSignals.profiles` on every destination, and
// `instrumentationRule.sourcesScopes` on every rule). Older proxies don't
// define those input fields, and gqlparser's variable coercion rejects any
// unknown input field with a bare "unknown field" error (the offending field
// is carried in the error `path`). `getVersionedQuery` only versions the query
// STRING — not the variables — so we reconcile input fields a proxy predates
// here, before they're sent over REMOTE_FETCH.
//
// Two strategies:
//   1. STRIP — drop a field the proxy predates. Safe because below the
//      introduced version the field doesn't exist anywhere in the proxy's
//      schema, so a by-name deep strip can never remove a supported field.
//   2. MAP — `sourcesScopes` (rule input, v1.26+) is rewritten to the legacy
//      `workloads: [PodWorkloadInput!]` field for pre-v1.26 proxies so rules
//      stay scoped instead of silently matching everything. Only fully
//      specified workloads (namespace+kind+name) have a legacy equivalent;
//      namespace-only / language-only criteria are dropped (the rule matches
//      more broadly for those — the legacy schema has no way to express them).
//
// All branching only applies to a real (`> v0.0.0`) selected proxy.
// ---------------------------------------------------------------------------
const STRIP_INPUT_FIELDS: { name: string; minVersion: number }[] = [
  { name: 'profiles', minVersion: trimVersion('v1.26') }, // ExportedSignalsInput.profiles
  { name: 'fromSources', minVersion: trimVersion('v1.14') }, // K8s(Label|Annotation)AttributeInput.fromSources
  { name: 'urlTemplatizationRulesGroups', minVersion: trimVersion('v1.20') }, // ActionFieldsInput.urlTemplatizationRulesGroups
  { name: 'otelDistroName', minVersion: trimVersion('v1.27') }, // PatchSourceRequestInput.otelDistroName
  { name: 'extractAttribute', minVersion: trimVersion('v1.29') }, // ActionFieldsInput.extractAttribute
];

// `instrumentationRule.sourcesScopes` (v1.26+) -> legacy `workloads` (<=v1.25).
const SOURCES_SCOPES_MIN_VERSION = trimVersion('v1.26');

const sourcesScopesToWorkloads = (rows: unknown): { namespace: string; kind: string; name: string }[] => {
  if (!Array.isArray(rows)) return [];
  const out: { namespace: string; kind: string; name: string }[] = [];
  for (const r of rows) {
    const row = r as { workloadName?: string | null; workloadKind?: string | null; workloadNamespace?: string | null } | null;
    // PodWorkloadInput requires all three fields; rows missing any (namespace-
    // or language-only criteria) have no legacy representation and are dropped.
    if (row?.workloadName && row.workloadKind && row.workloadNamespace) {
      out.push({ namespace: row.workloadNamespace, kind: row.workloadKind, name: row.workloadName });
    }
  }
  return out;
};

const transformDrift = (value: unknown, strip: Set<string>, mapScopes: boolean): unknown => {
  if (Array.isArray(value)) return value.map((v) => transformDrift(v, strip, mapScopes));
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (strip.has(k)) continue;
      if (mapScopes && k === 'sourcesScopes') {
        const workloads = sourcesScopesToWorkloads(v);
        if (workloads.length) out.workloads = workloads;
        continue;
      }
      out[k] = transformDrift(v, strip, mapScopes);
    }
    return out;
  }
  return value;
};

// Reconcile mutation inputs with the selected proxy's schema so legacy proxies
// don't reject the operation with "unknown field". No-op for modern proxies and
// for the out-of-proxy (`v0.0.0`) context.
const sanitizeVariablesForVersion = (vars: unknown, ctx: OperationContext): Record<string, unknown> => {
  const base = (vars ?? {}) as Record<string, unknown>;
  // These are k8s odigos-version drift shims (field stripping / scope mapping).
  // VM dialect documents are already correct per schema version, and a VM v0.x
  // version would otherwise falsely trip every `minVersion` gate, so this is a
  // no-op for non-k8s platforms.
  if (ctx.platformType !== PlatformType.K8s) return base;
  const v = trimVersion(ctx.version);
  if (!(v > 0)) return base;
  const strip = new Set(STRIP_INPUT_FIELDS.filter((f) => v < f.minVersion).map((f) => f.name));
  const mapScopes = v < SOURCES_SCOPES_MIN_VERSION;
  if (!strip.size && !mapScopes) return base;
  return transformDrift(base, strip, mapScopes) as Record<string, unknown>;
};

// VM proxies resolve their GraphQL document straight from the published vmagent
// dialect registry (floored by `ctx.schemaVersion`), not the hand-written k8s
// `VersionedRemoteFetch` buckets. A `VmSpec` is how a single operation maps onto
// that surface:
//   - `select`        — pick a static dialect document (reads + single mutations).
//   - `buildQuery`    — build a query string dynamically for a bulk op with no
//                       single VM document (e.g. PERSIST_SOURCES fans out into
//                       one combined, aliased create/delete mutation).
//   - `transformVariables` / `transformResult` — shape-map the agents-api
//                       request/response to the kit's slot shape.
//   - `unsupported`   — VM has no equivalent (e.g. namespace persist); makes
//                       `canRun` short-circuit on VM proxies.
// `select` and `buildQuery` are mutually exclusive. Operations without a VmSpec
// keep their k8s-only behavior (no VM bucket -> `canRun` is false on VM).
interface VmSpec<TData, TVars> {
  select?: VmSelect;
  buildQuery?: (vars: TVars, ctx: OperationContext) => string;
  // Optional post-process of the printed agents-api document (e.g. inject fields
  // the frozen dialect snapshot omits but a newer schemaVersion supports).
  patchQuery?: (query: string, ctx: OperationContext) => string;
  transformVariables?: (vars: TVars, ctx: OperationContext) => Record<string, unknown>;
  transformResult?: (data: unknown, ctx: OperationContext) => TData | undefined;
  unsupported?: boolean;
}

// Generic `<TData, TVars>` lets each call site declare the exact
// server-side response and request shapes for that operation, so
// every kit slot's `Operation<TData, TVars>` lines up. We deliberately
// avoid `= unknown` defaults here — every call site MUST specify
// both, which keeps the slot type strictly typed end-to-end (any
// missing-generic call site becomes a TypeScript error rather than
// silently degrading to `unknown` data flowing through the kit).
const wrapRemote = <TData, TVars>(versioned: VersionedRemoteFetch, extract?: (envelope: unknown) => TData | undefined, vm?: VmSpec<TData, TVars>): Operation<TData, TVars> => {
  // `isVm` is a misnomer kept for brevity: it's true for any agents-api platform
  // (VM agent OR cloud connector) that has a `VmSpec` to resolve from the dialect.
  const isVm = (ctx: OperationContext): boolean => isAgentsApiPlatform(ctx) && !!vm;

  // Resolve the GraphQL query string for the active context. VM ops resolve
  // from the dialect registry (`select`) or build it dynamically (`buildQuery`,
  // which needs the variables); k8s floors the `VersionedRemoteFetch` bucket map.
  const resolveQuery = (ctx: OperationContext, vars?: TVars): string | undefined => {
    if (isVm(ctx)) {
      if (vm!.unsupported) return undefined;
      let query: string | undefined;
      if (vm!.buildQuery) query = vars === undefined ? undefined : vm!.buildQuery(vars, ctx);
      else if (vm!.select) query = vmQuery(ctx, vm!.select);
      if (query && vm!.patchQuery) query = vm!.patchQuery(query, ctx);
      return query;
    }
    return getVersionedQuery(versioned, ctx.platformType, dialectVersion(ctx));
  };

  const resolveVariables = (vars: TVars, ctx: OperationContext): Record<string, unknown> => {
    if (isVm(ctx) && vm!.transformVariables) return vm!.transformVariables(vars, ctx);
    return sanitizeVariablesForVersion(vars, ctx);
  };

  const mapResult = (innerData: unknown, ctx: OperationContext): TData | undefined => {
    if (isVm(ctx) && vm!.transformResult) return vm!.transformResult(innerData, ctx);
    return extract ? extract(innerData) : (innerData as TData);
  };

  return {
    document: REMOTE_FETCH,
    // Every REMOTE_FETCH op needs a target proxy: either the currently
    // selected one (`ctx.proxyID`, in-proxy pages) OR a connections-scope
    // selection (`ctx.connectionIds`, the bulk drawers + EditConfigDrawer on
    // `/connections`). On out-of-proxy pages with NEITHER (sign-in,
    // `/access-management`, the bare connections list before any selection)
    // `ctx.proxyID` is undefined and there's no version to resolve, so
    // dispatching would send an empty `RemoteFetch($proxyID, $query,
    // $variables)` with both required vars missing — the backend rejects it,
    // and if the store is reset mid-flight (proxy navigation) Apollo throws
    // "Store reset while query was in flight". The kit honors this guard by
    // skipping the query entirely (no network request, no observer).
    canRun: (ctx) => {
      if (!resolveProxyID(ctx)) return false;
      if (isVm(ctx)) {
        if (vm!.unsupported) return false;
        // `buildQuery` ops can't form their query without vars at canRun time;
        // the dialect always resolves, so a selected VM proxy is enough.
        return !!vm!.buildQuery || !!resolveQuery(ctx);
      }
      return !!getVersionedQuery(versioned, ctx.platformType, dialectVersion(ctx));
    },
    transformVariables: (vars, ctx) => ({
      // In connections scope there's no single "selected" proxy — the bulk
      // drawers pass `connectionIds`. We use the first as the coordinator
      // proxy the REMOTE_FETCH is addressed to; ops that genuinely fan out
      // server-side (e.g. `updateRemoteConfig`) also receive the full
      // `connectionIds` list inside their own `variables`.
      proxyID: resolveProxyID(ctx),
      query: resolveQuery(ctx, vars),
      variables: resolveVariables(vars, ctx),
    }),
    transformResult: (raw, ctx): TData | undefined => {
      const remoteFetch = (raw as RemoteFetchResponse | null | undefined)?.remoteFetch;
      if (typeof remoteFetch !== 'string') return undefined;
      const parsed = safeJsonParse<RemoteParsedPayload<TData>>(remoteFetch, { data: undefined });
      // Inner GraphQL errors are nested at `data.errors`; never surface that
      // envelope as valid data (the `remoteFetchErrorLink` lifts these to a
      // top-level Apollo error so the kit toasts the failure).
      if (innerRemoteErrors(parsed).length) return undefined;
      if (!parsed?.data) return undefined;
      return mapResult(parsed.data, ctx);
    },
    documentMulti: REMOTE_MULTI_FETCH,
    // Group all proxies under the active context's version. The legacy
    // `useRemoteFetch.fetchMulti` supported mixed-version fan-out by reading
    // each proxy's reported version from the connections list and grouping
    // accordingly; that subtlety is preserved as a follow-up so the kit's
    // runner stays simple. For now the fan-out uses ctx.version + ctx.platformType,
    // which matches today's behavior for in-proxy CRUD calls.
    transformVariablesMulti: (proxyIDs, vars, ctx): MultiFetchGroup[] => {
      const queryString = resolveQuery(ctx, vars as TVars);
      if (!queryString) return [];

      return [
        {
          document: REMOTE_MULTI_FETCH,
          variables: { proxyIDs, query: queryString, variables: resolveVariables(vars as TVars, ctx) },
          proxyIDs,
        },
      ];
    },
    // Map the REMOTE_MULTI_FETCH response array of `{ proxyID, success, data, error }`
    // into the kit's `MultiFetchResult[]`, parsing the stringified JSON `data` per row.
    transformMultiResult: (raw, ctx): MultiFetchResult<TData>[] => {
      const rows = (raw as RemoteMultiFetchResponse | null | undefined)?.remoteMultiFetch ?? [];
      return rows.map((row) => {
        const parsed = row.data ? safeJsonParse<RemoteParsedPayload<TData>>(row.data, {}) : undefined;
        // Per-proxy GraphQL errors are nested at `data.errors` inside each row's
        // stringified payload (the top-level `errors` sibling is typically empty);
        // treat their presence as a failed row rather than silently dropping data.
        const innerErrors = innerRemoteErrors(parsed);
        const errFromApi = innerErrors.length ? innerErrors.join(', ') : undefined;
        const data = parsed?.data && !innerErrors.length ? mapResult(parsed.data, ctx) : undefined;
        return {
          proxyID: row.proxyID,
          success: row.success && !innerErrors.length,
          data,
          error: errFromApi ?? row.error,
        };
      });
    },
  };
};

// ---------------------------------------------------------------------------
// Legacy (< v1.20) proxy support.
//
// All version branching lives HERE, in the host adapter — the kit stays
// version-agnostic. The kit always invokes the modern slot (e.g.
// `GET_WORKLOADS`); `wrapVersioned` decides, per `ctx.version`, whether to
// send the modern versioned query or a legacy combined query + shape-map.
// ---------------------------------------------------------------------------

// `trimVersion('v1.20')` → numeric floor used to gate the legacy paths.
const V120 = trimVersion('v1.20');

// The legacy (< v1.20) combined-query path is a k8s-only concern. VM proxies
// report v0.x agent versions that would otherwise all read as "legacy k8s", so
// gate on platform first — VM never takes the k8s legacy path.
const isLegacyProxy = (ctx: OperationContext): boolean => ctx.platformType === PlatformType.K8s && trimVersion(ctx.version) < V120;

// @deprecated-legacy-api(v1.20) — true only when a *real, selected* proxy is
// older than v1.20. Excludes the `v0.0.0` "no proxy selected" default
// (`trimVersion > 0`) so the out-of-proxy central aggregate (e.g.
// `GET_ALL_CLUSTER_SNAPSHOTS` on the connections overview) still runs.
const isLegacySelectedProxy = (ctx: OperationContext): boolean => !!resolveProxyID(ctx) && trimVersion(ctx.version) > 0 && isLegacyProxy(ctx);

// Extract any GraphQL error messages from a decoded REMOTE_FETCH payload.
// The proxy nests its inner GraphQL response under `data`, so a failed call
// looks like `{ data: { errors: [{ message }] } }` (the top-level `errors`
// sibling stays empty). We check `data.errors` first, then the sibling.
const innerRemoteErrors = (parsed: RemoteParsedPayload<unknown> | undefined): string[] => {
  const nested = (parsed?.data as { errors?: { message?: string }[] } | null | undefined)?.errors;
  const errors = nested?.length ? nested : parsed?.errors;
  return Array.isArray(errors) ? errors.map((e) => e?.message || 'Unknown error') : [];
};

// Decode the stringified-JSON `remoteFetch` envelope into its inner `data`.
// Returns undefined when the payload carries inner GraphQL errors so legacy
// shape-mappers never run against an error envelope.
const parseRemoteFetchData = <T,>(raw: unknown): T | undefined => {
  const remoteFetch = (raw as RemoteFetchResponse | null | undefined)?.remoteFetch;
  if (typeof remoteFetch !== 'string') return undefined;
  const parsed = safeJsonParse<RemoteParsedPayload<T>>(remoteFetch, { data: undefined });
  if (innerRemoteErrors(parsed).length) return undefined;
  return parsed?.data;
};

// Apollo link that surfaces inner REMOTE_FETCH GraphQL errors. The transport
// wraps each proxy response in a stringified `remoteFetch` field; when the
// inner GraphQL call fails the OUTER response is still HTTP/GraphQL "success",
// so without this the kit toasts success on a failed mutation/query. We decode
// the payload, lift any inner errors to top-level Apollo `errors`, and let the
// client's default `errorPolicy` reject the operation so the kit shows the
// error toast. Single REMOTE_FETCH only — REMOTE_MULTI_FETCH keeps per-row
// error handling in `transformMultiResult` (one bad proxy must not fail the batch).
const remoteFetchErrorLink = new ApolloLink((operation, forward) =>
  forward(operation).pipe(
    map((response) => {
      const remoteFetch = (response.data as RemoteFetchResponse | null | undefined)?.remoteFetch;
      if (typeof remoteFetch !== 'string') return response;
      const parsed = safeJsonParse<RemoteParsedPayload<unknown>>(remoteFetch, { data: undefined });
      const messages = innerRemoteErrors(parsed);
      if (!messages.length) return response;
      return { ...response, errors: [...(response.errors ?? []), ...messages.map((message) => new GraphQLError(message))] };
    }),
  ),
);

// Execute a single REMOTE_FETCH-backed `Operation` imperatively (mirrors the
// kit runner's `transformVariables -> client.query -> transformResult` single
// call). Used by legacy `run` executors to perform their round-trips entirely
// host-side without re-entering the kit runner.
const runRemoteSingle = async <TData, TVars>(client: ApolloClient, op: Operation<TData, TVars>, vars: TVars | undefined, ctx: OperationContext): Promise<{ data?: TData; error?: string }> => {
  const document = typeof op.document === 'function' ? op.document(ctx) : op.document;
  if (!document) return { error: 'Operation not supported in this context' };
  const variables = op.transformVariables ? op.transformVariables(vars as TVars, ctx) : (vars as Record<string, unknown> | undefined);
  const result = await client.query({ query: document, variables, fetchPolicy: 'network-only', errorPolicy: 'all' });
  const data = op.transformResult ? op.transformResult(result.data, ctx) : (result.data as TData);
  const error = result.error ? (CombinedGraphQLErrors.is(result.error) ? result.error.errors.map((e) => e.message).join(', ') : result.error.message) : undefined;
  return { data, error };
};

/**
 * @deprecated-legacy-api(v1.20) — describes how a modern slot resolves on a
 * proxy older than v1.20. Two mutually-compatible strategies:
 *
 *   - Single-query (`query` + `map`): swap in a combined legacy
 *     `VersionedRemoteFetch` query string and map its parsed envelope into the
 *     modern `TData`. Works for BOTH the declarative `useApiQuery` path and the
 *     imperative runner, because it stays a single REMOTE_FETCH call.
 *
 *   - Multi-step (`run`): a full custom imperative executor for legacy shapes
 *     that genuinely need several round-trips (e.g. source-by-id: fetch each
 *     `GET_SOURCE` then enrich agent-injection from `GET_WORKLOADS`). Consumed
 *     ONLY by the imperative runner via `Operation.run`.
 */
interface LegacySpec<TData, TVars> {
  query?: VersionedRemoteFetch;
  variables?: (vars: TVars | undefined, ctx: OperationContext) => Record<string, unknown>;
  map?: (legacyData: unknown, ctx: OperationContext) => TData | undefined;
  run?: (client: ApolloClient, vars: TVars | undefined, ctx: OperationContext) => Promise<{ data?: TData; error?: string }>;
}

/**
 * The single funnel every operation in the map is constructed through.
 *
 * Without a `legacy` arg it's exactly `wrapRemote` (modern-only) — routing
 * even shape-identical ops through here keeps the construction uniform and
 * makes adding a legacy branch later a one-line change.
 *
 * With a `legacy` arg it produces a version-aware `Operation`:
 *   - `>= v1.20`: the modern `wrapRemote` behavior (versioned query + extract).
 *   - `< v1.20`: the legacy single-query (`query`/`map`) and/or multi-step
 *     (`run`) behavior. `canRun` additionally requires the legacy query string
 *     to resolve for single-query legacy ops.
 */
const wrapVersioned = <TData, TVars>(
  modern: VersionedRemoteFetch,
  extract?: (envelope: unknown) => TData | undefined,
  legacy?: LegacySpec<TData, TVars>,
  vm?: VmSpec<TData, TVars>,
): Operation<TData, TVars> => {
  const base = wrapRemote<TData, TVars>(modern, extract, vm);
  if (!legacy) return base;

  const op: Operation<TData, TVars> = {
    ...base,
    canRun: (ctx) => {
      if (!resolveProxyID(ctx)) return false;
      if (isLegacyProxy(ctx)) {
        // `run` executors resolve their own per-step queries; single-query
        // legacy needs its combined query string to exist for this version.
        if (legacy.run) return true;
        return !!legacy.query && !!getVersionedQuery(legacy.query, ctx.platformType, dialectVersion(ctx));
      }
      // Modern path (incl. VM): defer to the base op, which is dialect/VM-aware.
      return base.canRun!(ctx);
    },
  };

  if (legacy.query && legacy.map) {
    op.transformVariables = (vars, ctx) => {
      if (isLegacyProxy(ctx)) {
        return {
          proxyID: resolveProxyID(ctx),
          query: getVersionedQuery(legacy.query!, ctx.platformType, dialectVersion(ctx)),
          variables: legacy.variables ? legacy.variables(vars, ctx) : {},
        };
      }
      return base.transformVariables!(vars, ctx);
    };
    op.transformResult = (raw, ctx) => {
      if (isLegacyProxy(ctx)) {
        const data = parseRemoteFetchData<unknown>(raw);
        if (data === undefined) return undefined;
        return legacy.map!(data, ctx);
      }
      return base.transformResult!(raw, ctx);
    };
  }

  if (legacy.run) {
    // The kit runner consults `run` for ALL versions when present, so the
    // modern (>= v1.20) imperative path is handled here too via a single call.
    op.run = async (client, vars, ctx) => {
      if (isLegacyProxy(ctx)) return legacy.run!(client, vars, ctx);
      return runRemoteSingle(client, base, vars, ctx);
    };
  }

  return op;
};

// @deprecated-legacy-api(v1.20) — merge the legacy `workloads(filter)`
// agent-injection / health rows into a list of mapped `Workload`s (by id).
const mergeAgentInjection = (workloads: Workload[], injection: WorkloadWithOdigosHealthStatus[] | undefined): Workload[] => {
  if (!injection?.length) return workloads;
  const keyOf = (id: WorkloadId) => `${id.namespace}/${id.kind}/${id.name}`;
  const byKey = new Map(workloads.map((w) => [keyOf(w.id), w]));
  for (const item of injection) {
    const updated = addAgentInjectionStatusToSources(item, workloads);
    if (updated) byKey.set(keyOf(updated.id), updated);
  }
  return Array.from(byKey.values());
};

// @deprecated-legacy-api(v1.20) — legacy "sources list" map: flat
// `K8sActualSource`s -> `Workload`s, enriched with agent-injection from the
// combined query's `workloads` selection (absent on VM, which is sources-only).
const legacySourcesToWorkloads = (legacyData: unknown): GetWorkloadsData => {
  const env = legacyData as { computePlatform?: { sources?: LegacySource[] }; workloads?: WorkloadWithOdigosHealthStatus[] } | null | undefined;
  const sources = env?.computePlatform?.sources ?? [];
  const workloads = sources.map(mapLegacySourceToWorkload);
  return { workloads: mergeAgentInjection(workloads, env?.workloads) };
};

// @deprecated-legacy-api(v1.20) — legacy source-by-id executor. `workloadsByIds`
// / `K8sWorkloadIdInput` only exist on v1.21+, so for older proxies we resolve
// each id through the combined `GET_SOURCE_WITH_DETAILS` query — a single
// REMOTE_FETCH round-trip that folds the old standalone GET_SOURCE_BY_ID +
// GET_SOURCE_CONDITIONS + GET_SOURCE_LIBRARIES + workloads queries together —
// then maps the envelope into the v1.20+ `Workload` shape. This keeps the
// source drawer's data (containers, conditions, agent-injection, instrumentation
// libraries) intact on legacy proxies, matching `GET_WORKLOADS_BY_IDS`.
const runLegacySourcesByIds = async (client: ApolloClient, vars: GetWorkloadsByIdsVars | undefined, ctx: OperationContext): Promise<{ data?: GetWorkloadsByIdsData; error?: string }> => {
  const ids = vars?.ids ?? [];
  if (!ids.length) return { data: { workloadsByIds: [] } };

  const sourceOp = wrapRemote<CombinedLegacySourceEnvelope, { sourceId: WorkloadId; namespace: string; kind: string; name: string }>(GET_SOURCE_WITH_DETAILS);
  const fetched: Workload[] = [];
  for (const id of ids) {
    const res = await runRemoteSingle(
      client,
      sourceOp,
      { sourceId: { namespace: id.namespace, kind: id.kind, name: id.name } as WorkloadId, namespace: id.namespace, kind: id.kind, name: id.name },
      ctx,
    );
    const workload = mapCombinedLegacySourceToWorkload(res.data);
    if (workload) fetched.push(workload);
  }

  return { data: { workloadsByIds: fetched } };
};

// @deprecated-legacy-api(v1.20) — legacy "namespaces with workloads" map:
// flat `K8sActualNamespace`s (with nested `sources`) -> modern `Namespace`s.
const legacyNamespacesToNamespaces = (legacyData: unknown): GetNamespacesWithWorkloadsData => {
  const list = (legacyData as { computePlatform?: { k8sActualNamespaces?: LegacyK8sActualNamespace[] } } | null | undefined)?.computePlatform?.k8sActualNamespaces ?? [];
  return { namespaces: list.map(legacyNamespaceToNamespace) };
};

// The backend's `ActionFieldsInput.renames` is a JSON-stringified `String`
// (it `json.Unmarshal`s the value server-side), while the kit models
// `renames` as an object map. Normalize on the way out (create/update) and
// parse on the way back (list) so the kit's RenameAttribute form always sees
// the object shape. Catalog-driven forms can also emit `renames` as a
// `[{ key, value }]` row array and ExtractAttribute rows with UI-only /
// empty optional fields — `normalizeActionForWire` handles both. Without
// this the raw object/array is sent to a `String` scalar and the mutation
// is rejected (no CRD is created). `withActionRenames` composes this around
// `wrapRemote` so it survives the REMOTE_FETCH round-trip.
const parseActionRenames = (action: Action): Action => {
  const renames = action?.fields?.renames as unknown;
  if (typeof renames !== 'string') return action;
  return { ...action, fields: { ...action.fields, renames: parseRenamesString(renames) } };
};

const parseActionRenamesInData = <T,>(data: T | undefined): T | undefined => {
  if (!data || typeof data !== 'object') return data;
  const d = data as {
    computePlatform?: { actions?: Action[] };
    actions?: Action[];
    createAction?: Action;
    updateAction?: Action;
  };
  if (Array.isArray(d.computePlatform?.actions)) {
    return { ...d, computePlatform: { ...d.computePlatform, actions: d.computePlatform!.actions!.map(parseActionRenames) } } as T;
  }
  if (Array.isArray(d.actions)) {
    return { ...d, actions: d.actions.map(parseActionRenames) } as T;
  }
  if (d.createAction) return { ...d, createAction: parseActionRenames(d.createAction) } as T;
  if (d.updateAction) return { ...d, updateAction: parseActionRenames(d.updateAction) } as T;
  return data;
};

// Root-level safety net for the kit's "transformVariables runs outside the
// runner's try/catch" gap.
//
// The kit executes an operation one of two ways:
//   - via `Operation.run`, which it ALWAYS wraps in try/catch, OR
//   - via its default `transformVariables -> client.{query,mutate}` path, where
//     `transformVariables` is called BEFORE the try block.
//
// So any throw from a `transformVariables` (e.g. a VM builder rejecting an
// action type with no VM equivalent, or a rule with an empty required
// collection) escapes as an unhandled rejection that silently aborts the
// save/refetch with only a console error. By giving EVERY operation a `run`,
// we move execution onto the guarded path for all operations — current and
// future — so validation errors always surface as normal error toasts without
// every call site having to remember to opt in.
//
// `run` delegates to `runRemoteSingle` (the same single-REMOTE_FETCH executor
// the legacy paths already use): for reads it's identical to the kit's default
// (`client.query`, network-only); for the REMOTE_FETCH-wrapped "mutations" the
// outer document is a `query RemoteFetch` either way, so the wire request is
// unchanged. Operations that already define their own `run` (the legacy
// multi-step executors) are left untouched — the kit already guards those.
const withGuardedRun = <TData, TVars>(op: Operation<TData, TVars>): Operation<TData, TVars> => {
  if (op.run) return op;
  return {
    ...op,
    run: async (client, vars, ctx) => {
      try {
        return await runRemoteSingle(client, op, vars, ctx);
      } catch (err) {
        return { error: err instanceof Error ? err.message : String(err) };
      }
    },
  };
};

// Apply `withGuardedRun` to every operation in the map so the guard is a root
// behavior of the adapter rather than an opt-in per operation.
const withGuardedRuns = (ops: OdigosApiOperations): OdigosApiOperations => {
  const guarded = {} as Record<string, Operation<unknown, unknown> | undefined>;
  for (const [key, op] of Object.entries(ops)) {
    guarded[key] = op ? (withGuardedRun(op as Operation<unknown, unknown>) as Operation<unknown, unknown>) : op;
  }
  return guarded as unknown as OdigosApiOperations;
};

// After source persist/update, refresh GET_WORKLOADS via its run() path (snapshot
// + RemoteFetch fallback) so overview useSources (cache-first on GET_CLUSTER_SNAPSHOT)
// sees selected workloads before Redis catches up.
const withSourceMutationWorkloadsRefresh = (ops: OdigosApiOperations): OdigosApiOperations => {
  const refreshWorkloads = async (client: ApolloClient, ctx: OperationContext, attempts: number) => {
    const getWorkloads = ops.GET_WORKLOADS;
    if (!getWorkloads?.run) return;
    for (let attempt = 0; attempt < attempts; attempt++) {
      try {
        const result = await getWorkloads.run(client, { filter: { markedForInstrumentation: true } }, ctx);
        if (result?.data?.workloads?.length || attempt === attempts - 1) return;
      } catch {
        if (attempt === attempts - 1) return;
      }
      await new Promise((r) => setTimeout(r, 500));
    }
  };

  const wrapMutation = <TData, TVars>(op: Operation<TData, TVars> | undefined, attempts: number): Operation<TData, TVars> | undefined => {
    if (!op?.run) return op;
    const prevRun = op.run;
    return {
      ...op,
      run: async (client, vars, ctx) => {
        const result = await prevRun(client, vars, ctx);
        if (!result.error) await refreshWorkloads(client, ctx, attempts);
        return result;
      },
    };
  };

  return {
    ...ops,
    // Persist may race IC creation — retry briefly. Update already has ICs.
    PERSIST_SOURCES: wrapMutation(ops.PERSIST_SOURCES, 5)!,
    UPDATE_SOURCE: wrapMutation(ops.UPDATE_SOURCE, 2)!,
  };
};

// Wrap a REMOTE_FETCH-backed action op so catalog form payloads are normalized
// before the inner variables are embedded, and `renames` is parsed back out of
// the inner response. Reads (GET_ACTIONS) pass through variables unchanged and
// only need the response-side parse.
const withActionRenames = <TData, TVars>(op: Operation<TData, TVars>): Operation<TData, TVars> => ({
  ...op,
  transformVariables: op.transformVariables
    ? (vars, ctx) => {
        const hasAction = !!vars && typeof vars === 'object' && 'action' in (vars as object) && !!(vars as { action?: unknown }).action;
        const next = hasAction ? (normalizeActionForWire(vars as { action?: { fields?: Record<string, unknown> } }) as TVars) : vars;
        return op.transformVariables!(next, ctx);
      }
    : undefined,
  transformResult: (raw, ctx) => parseActionRenamesInData(op.transformResult ? op.transformResult(raw, ctx) : (raw as TData | undefined)),
});

// Wrap the remote-config mutation so the kit's `{ formData, connectionIds }`
// slot input is translated into the proxy's `$config: RemoteConfigInput!`
// shape before the inner variables are embedded in the RemoteFetch. The proxy
// applies the config to its own cluster; `connectionIds` only selects which
// proxies the RemoteFetch is addressed to (resolved from `ctx`), so it is
// intentionally dropped from the inner variables. `RemoteConfigInput` is
// `Pick<EffectiveConfigInput, 'rollout'>`, so only `rollout` is forwarded.
const withRemoteConfigVars = <TData,>(op: Operation<TData, UpdateRemoteConfigVars>): Operation<TData, UpdateRemoteConfigVars> => {
  const toWire = (vars: UpdateRemoteConfigVars | undefined) => ({ config: { rollout: vars?.formData?.rollout } }) as unknown as UpdateRemoteConfigVars;
  return {
    ...op,
    transformVariables: op.transformVariables ? (vars, ctx) => op.transformVariables!(toWire(vars), ctx) : undefined,
    transformVariablesMulti: op.transformVariablesMulti ? (proxyIDs, vars, ctx) => op.transformVariablesMulti!(proxyIDs, toWire(vars), ctx) : undefined,
  };
};

const operations: OdigosApiOperations = withSourceMutationWorkloadsRefresh(
  withGuardedRuns({
  // sources / workloads
  //
  // Legacy < v1.20: the kit always asks for `GET_WORKLOADS`; here it resolves
  // to a single combined query (`computePlatform.sources` + `workloads`
  // injection rows) mapped into `Workload`s. By-id resolves via a multi-step
  // `run` (one `GET_SOURCE` per id + a single agent-injection enrichment).
  //
  // Prefer Redis cluster snapshots for modern k8s list loads (PLAT-1302): avoids
  // multi-MB GetWorkloads RemoteFetch responses. When Redis lags IC creation,
  // imperative run() falls back to RemoteFetch, merges into the full snapshot,
  // writeQuerys GET_CLUSTER_SNAPSHOT (useSources is cache-first on that key),
  // and keeps an in-memory overlay so declarative transformResult stays fresh.
  // Legacy (< v1.20) and VM/connector keep RemoteFetch. Detail drawers still use
  // GET_WORKLOADS_BY_IDS RemoteFetch for full field parity.
  GET_WORKLOADS: (() => {
    const remote = wrapVersioned<GetWorkloadsData, GetWorkloadsVars | undefined>(
      GET_WORKLOADS,
      undefined,
      { query: GET_SOURCES_WITH_STATUS, map: (data) => legacySourcesToWorkloads(data) },
      {
        buildQuery: (_vars, ctx) => buildVmSourcesWithReportsQuery(ctx) ?? '',
        transformResult: (data) => mapVmSourcesWithReportsToWorkloads(data),
      },
    );

    const prefersSnapshot = (ctx: OperationContext) => !isAgentsApiPlatform(ctx) && !isLegacyProxy(ctx) && !!resolveProxyID(ctx);

    const writeSnapshotCache = (client: ApolloClient, clusterId: string, snapshot: SnapshotCluster) => {
      setWorkloadSnapshotOverlay(clusterId, snapshot);
      try {
        client.writeQuery({
          query: GET_CLUSTER_SNAPSHOT as DocumentNode,
          variables: { clusterId },
          data: { clusterSnapshot: snapshot },
        });
      } catch {
        // Cache write is best-effort; overlay still covers transformResult.
      }
    };

    const op: Operation<GetWorkloadsData, GetWorkloadsVars | undefined> = {
      ...remote,
      document: (ctx) => (prefersSnapshot(ctx) ? (GET_CLUSTER_SNAPSHOT as DocumentNode) : typeof remote.document === 'function' ? remote.document(ctx) : remote.document),
      canRun: (ctx) => {
        if (prefersSnapshot(ctx)) return true;
        return remote.canRun ? remote.canRun(ctx) : true;
      },
      transformVariables: (vars, ctx) => {
        if (prefersSnapshot(ctx)) return { clusterId: resolveProxyID(ctx) };
        return remote.transformVariables!(vars, ctx);
      },
      transformResult: (raw, ctx) => {
        if (prefersSnapshot(ctx)) {
          const clusterId = resolveProxyID(ctx);
          const redis = (raw as { clusterSnapshot?: SnapshotCluster } | null | undefined)?.clusterSnapshot;
          const snapshot = resolveClusterSnapshot(clusterId, redis);
          // Overview always requests markedForInstrumentation=true; transformResult
          // has no vars, so apply that filter here for the declarative query path.
          return mapClusterSnapshotToWorkloads(snapshot, { filter: { markedForInstrumentation: true } });
        }
        return remote.transformResult!(raw, ctx);
      },
      run: async (client, vars, ctx) => {
        try {
          if (!prefersSnapshot(ctx)) {
            if (remote.run) return remote.run(client, vars, ctx);
            return runRemoteSingle(client, remote, vars, ctx);
          }

          const clusterId = resolveProxyID(ctx)!;
          const wantsMarked = vars?.filter?.markedForInstrumentation === true;
          let redis: SnapshotCluster | null | undefined;

          try {
            const result = await client.query({
              query: GET_CLUSTER_SNAPSHOT as DocumentNode,
              variables: { clusterId },
              fetchPolicy: 'network-only',
              errorPolicy: 'all',
            });
            redis = (result.data as { clusterSnapshot?: SnapshotCluster } | null | undefined)?.clusterSnapshot;
          } catch {
            // Fall through to RemoteFetch when snapshot is unavailable.
          }

          const snapshot = resolveClusterSnapshot(clusterId, redis);
          if (snapshot?.namespaces?.length) {
            const mapped = mapClusterSnapshotToWorkloads(snapshot, vars);
            // Snapshot can lag IC/Source creates. When the overview asks for
            // instrumented workloads and Redis/overlay still show none selected,
            // fall through to live RemoteFetch.
            if (!(wantsMarked && mapped.workloads.length === 0 && !snapshotHasSelectedWorkloads(snapshot))) {
              // Refresh Apollo so useSources (cache-first) sees this snapshot.
              writeSnapshotCache(client, clusterId, snapshot);
              return { data: mapped };
            }
          }

          const live = remote.run ? await remote.run(client, vars, ctx) : await runRemoteSingle(client, remote, vars, ctx);
          if (live.data?.workloads?.length) {
            const merged = mergeLiveSelectedIntoSnapshot(redis, clusterId, redis?.clusterName ?? clusterId, live.data.workloads);
            writeSnapshotCache(client, clusterId, merged);
          }
          return live;
        } catch (err) {
          return { error: err instanceof Error ? err.message : String(err) };
        }
      },
    };

    return op;
  })(),
  // VM: no by-id endpoint — resolve the flat agents-api `sources` list (plus
  // workloadReports for status) and map into `Workload`s under `workloadsByIds`.
  // The kit's `fetchById` `.find()`s the requested id out of the result (and SSE
  // `fetchByTargets` refreshes the list via `fetchAll` afterward), so returning
  // the full set is correct and cheap.
  GET_WORKLOADS_BY_IDS: wrapVersioned<GetWorkloadsByIdsData, GetWorkloadsByIdsVars>(
    GET_WORKLOADS_BY_IDS,
    undefined,
    { run: (client, vars, ctx) => runLegacySourcesByIds(client, vars, ctx) },
    {
      buildQuery: (_vars, ctx) => buildVmSourcesWithReportsQuery(ctx) ?? '',
      transformResult: (data) => ({ workloadsByIds: mapVmSourcesWithReportsToWorkloads(data).workloads }),
    },
  ),
  GET_WORKLOADS_BY_IDS_SLIM: wrapVersioned<GetWorkloadsByIdsData, GetWorkloadsByIdsVars>(
    GET_WORKLOADS_BY_IDS_SLIM,
    undefined,
    { run: (client, vars, ctx) => runLegacySourcesByIds(client, vars, ctx) },
    // Slim list path stays sources-only — overview table doesn't need full report payload.
    { select: (d) => d.GetSourcesDocument, transformResult: (data) => ({ workloadsByIds: mapVmSourcesToWorkloads(data).workloads }) },
  ),
  // VM: by-name `source(name)`; central-ui's `WorkloadId.name` is the source name.
  GET_SOURCE: wrapVersioned<{ source?: Workload }, WorkloadId>(GET_SOURCE, undefined, undefined, {
    select: (d) => d.GetSourceDocument,
    transformVariables: (vars) => ({ name: vars.name }),
    transformResult: (data) => mapVmSourceResult(data),
  }),
  GET_SOURCE_LIBRARIES: wrapVersioned<GetSourceLibrariesData, GetSourceLibrariesVars>(GET_SOURCE_LIBRARIES),
  GET_PEER_SOURCES: wrapVersioned<{ peerSources?: PeerSources }, GetPeerSourcesVars>(GET_PEER_SOURCES),
  // VM: no bulk persist op — fan out into one combined, aliased
  // createSource/deleteSource mutation (enable -> create, disable -> delete).
  PERSIST_SOURCES: wrapVersioned<PersistSourcesResult, PersistSourcesVars>(PERSIST_SOURCES, undefined, undefined, {
    buildQuery: (vars) => buildVmPersistSources(vars).query,
    transformVariables: (vars) => buildVmPersistSources(vars).variables,
    transformResult: () => ({ persistK8sSources: true }),
  }),
  // VM: `updateSource(input: UpdateSourceInput!)`.
  UPDATE_SOURCE: wrapVersioned<{ updateK8sActualSource: boolean }, UpdateSourceVars>(UPDATE_SOURCE, undefined, undefined, {
    select: (d) => d.UpdateSourceDocument,
    transformVariables: (vars) => vmUpdateSourceVariables(vars),
    transformResult: () => ({ updateK8sActualSource: true }),
  }),
  RESTART_WORKLOADS: wrapVersioned<{ restartWorkloads: boolean }, RestartWorkloadsVars>(RESTART_WORKLOADS),
  RESTART_POD: wrapVersioned<{ restartPod: boolean }, RestartPodVars>(RESTART_POD),
  RECOVER_FROM_ROLLBACK: wrapVersioned<{ recoverFromRollbackForWorkload: boolean }, RecoverFromRollbackVars>(RECOVER_FROM_ROLLBACK),

  // destinations
  // VM: the agents-api destination surface — `destinations`/`destinationTypes`
  // reads + per-destination create/update/delete/testConnection — resolved from
  // the versioned vmagent dialect registry and shape-mapped (see vm-destinations).
  GET_DESTINATIONS: wrapVersioned<GetDestinationsData, undefined>(GET_DESTINATIONS, undefined, undefined, {
    select: (d) => d.GetDestinationsDocument,
    transformResult: (data) => mapVmDestinations(data),
  }),
  // Kit slot types are `Operation<GetDestinationCategoriesResult, undefined>`
  // and `Operation<GetPotentialDestinationsResult, undefined>` — bind
  // them explicitly here so the kit consumer never sees `unknown`.
  // VM: `destinationTypes` (flat catalog) -> the kit's category catalog.
  GET_DESTINATION_CATEGORIES: wrapVersioned<GetDestinationCategoriesResult, undefined>(GET_DESTINATION_CATEGORIES, undefined, undefined, {
    select: (d) => d.GetDestinationTypesDocument,
    transformResult: (data) => mapVmDestinationTypesToCategories(data),
  }),
  // No VM equivalent for `potentialDestinations` (k8s auto-detect); VM `canRun`
  // is false (no VM bucket), so the op is simply unavailable there.
  GET_POTENTIAL_DESTINATIONS: wrapVersioned<GetPotentialDestinationsResult, undefined>(GET_POTENTIAL_DESTINATIONS),
  CREATE_DESTINATION: wrapVersioned<CreateDestinationResult, CreateDestinationVars>(CREATE_DESTINATION, undefined, undefined, {
    select: (d) => d.CreateDestinationDocument,
    transformVariables: (vars) => vmCreateDestinationVariables(vars),
    transformResult: (data) => vmCreateDestinationResult(data),
  }),
  UPDATE_DESTINATION: wrapVersioned<UpdateDestinationData, UpdateDestinationVars>(UPDATE_DESTINATION, undefined, undefined, {
    select: (d) => d.UpdateDestinationDocument,
    transformVariables: (vars) => vmUpdateDestinationVariables(vars),
    transformResult: (data) => vmUpdateDestinationResult(data),
  }),
  DELETE_DESTINATION: wrapVersioned<DeleteDestinationResult, DeleteDestinationVars>(DELETE_DESTINATION, undefined, undefined, {
    select: (d) => d.DeleteDestinationDocument,
    transformVariables: (vars) => vmDeleteDestinationVariables(vars),
    transformResult: (data) => vmDeleteDestinationResult(data),
  }),
  TEST_DESTINATION_CONNECTION: {
    // VM uses the agents-api `TestConnection` document + input; both k8s and VM
    // return the result under `testConnectionForDestination`, dug out below.
    ...wrapVersioned<TestConnectionResponse, TestDestinationConnectionVars>(TEST_DESTINATION_CONNECTION, undefined, undefined, {
      select: (d) => d.TestConnectionDocument,
      transformVariables: (vars) => vmTestConnectionVariables(vars),
    }),
    // The test-connection mutation's GraphQL shape returns the result
    // under `testConnectionForDestination`, not the wrapper's default
    // `data` field. Override `transformResult` to dig that one extra level.
    transformResult: (raw, ctx): TestConnectionResponse | undefined => {
      if (isAgentsApiPlatform(ctx)) return vmTestConnectionResult(parseRemoteFetchData(raw));
      const remoteFetch = (raw as RemoteFetchResponse | null | undefined)?.remoteFetch;
      if (typeof remoteFetch !== 'string') return undefined;
      const parsed = safeJsonParse<{ data?: { testConnectionForDestination?: TestConnectionResponse } }>(remoteFetch, { data: undefined });
      return parsed?.data?.testConnectionForDestination;
    },
  },

  // actions. Catalog form payloads (row-array `renames`, ExtractAttribute UI-only /
  // empty optional fields) are normalized for the wire, and stringified `renames`
  // are parsed back so the kit's object shape survives the proxy round-trip.
  // VM: only URL templatization maps across both surfaces — VM-only action types
  // (processorAddition / spanMetrics) are filtered out of reads and the k8s-only
  // kit types have no VM equivalent (see vm-actions). `renames` is a k8s-only
  // RenameAttribute concern, so `withActionRenames` is a no-op on VM.
  GET_ACTIONS: withActionRenames(
    wrapVersioned<GetActionsData, undefined>(GET_ACTIONS, undefined, undefined, {
      select: (d) => d.GetActionsDocument,
      transformResult: (data) => mapVmActions(data as GetActionsQuery),
    }),
  ),
  // Dynamic action-type catalog (YAML-driven). K8s: introduced in odigos v1.31 —
  // older proxies have no bucket, so `canRun` is false and the kit falls back to
  // its hard-coded ACTION_OPTIONS. VM: resolved from the vmagent `actionTypes`
  // dialect document (graceful no-op on dialects that predate it) and mapped to
  // the kit's catalog shape (VM-only types dropped, see vm-action-types).
  GET_ACTION_TYPES: wrapVersioned<GetActionTypesResult, undefined>(GET_ACTION_TYPES, undefined, undefined, {
    select: (d) => d.GetActionTypesDocument,
    transformResult: (data) => mapVmActionTypesToOptions(data),
  }),
  CREATE_ACTION: withActionRenames(
    wrapVersioned<CreateActionResult, CreateActionVars>(CREATE_ACTION, undefined, undefined, {
      select: (d) => d.CreateActionDocument,
      transformVariables: (vars) => vmCreateActionVariables(vars),
      transformResult: (data) => vmCreateActionResult(data as CreateActionMutation),
    }),
  ),
  UPDATE_ACTION: withActionRenames(
    wrapVersioned<UpdateActionResult, UpdateActionVars>(UPDATE_ACTION, undefined, undefined, {
      select: (d) => d.UpdateActionDocument,
      transformVariables: (vars) => vmUpdateActionVariables(vars),
      transformResult: (data) => vmUpdateActionResult(data as UpdateActionMutation),
    }),
  ),
  DELETE_ACTION: wrapVersioned<DeleteActionResult, DeleteActionVars>(DELETE_ACTION, undefined, undefined, {
    select: (d) => d.DeleteActionDocument,
    transformVariables: (vars) => vmDeleteActionVariables(vars),
    transformResult: (data) => vmDeleteActionResult(data as DeleteActionMutation),
  }),

  // instrumentation rules
  // VM: the agents-api rule surface — `instrumentationRules` read + per-rule
  // create/update/delete — resolved from the versioned vmagent dialect registry
  // and shape-mapped (see vm-instrumentation-rules). The VM rule model is
  // narrower than k8s (global rules, no scopes/notes/disabled, boolean
  // payload-collection), so those fields map to neutral defaults.
  GET_INSTRUMENTATION_RULES: wrapVersioned<GetInstrumentationRulesData, undefined>(GET_INSTRUMENTATION_RULES, undefined, undefined, {
    select: (d) => d.GetInstrumentationRulesDocument,
    transformResult: (data) => mapVmInstrumentationRules(data as GetInstrumentationRulesQuery),
  }),
  // Dynamic rule-type catalog (YAML-driven), mirroring GET_ACTION_TYPES. K8s:
  // introduced in odigos v1.31 — older proxies have no bucket, so `canRun` is
  // false and the kit falls back to its hard-coded INSTRUMENTATION_RULE_OPTIONS.
  // VM: resolved from the vmagent `instrumentationRuleTypes` dialect document
  // (graceful no-op on dialects that predate it) and mapped to the kit's catalog
  // shape (VM-only types dropped, see vm-rule-types).
  GET_INSTRUMENTATION_RULE_TYPES: wrapVersioned<GetInstrumentationRuleTypesResult, undefined>(GET_INSTRUMENTATION_RULE_TYPES, undefined, undefined, {
    select: (d) => d.GetInstrumentationRuleTypesDocument,
    transformResult: (data) => mapVmRuleTypesToOptions(data),
  }),
  CREATE_INSTRUMENTATION_RULE: wrapVersioned<CreateInstrumentationRuleResult, CreateInstrumentationRuleVars>(CREATE_INSTRUMENTATION_RULE, undefined, undefined, {
    select: (d) => d.CreateInstrumentationRuleDocument,
    transformVariables: (vars) => vmCreateInstrumentationRuleVariables(vars),
    transformResult: (data) => vmCreateInstrumentationRuleResult(data as CreateInstrumentationRuleMutation),
  }),
  UPDATE_INSTRUMENTATION_RULE: wrapVersioned<UpdateInstrumentationRuleResult, UpdateInstrumentationRuleVars>(UPDATE_INSTURMENTATION_RULE, undefined, undefined, {
    select: (d) => d.UpdateInstrumentationRuleDocument,
    transformVariables: (vars) => vmUpdateInstrumentationRuleVariables(vars),
    transformResult: (data) => vmUpdateInstrumentationRuleResult(data as UpdateInstrumentationRuleMutation),
  }),
  DELETE_INSTRUMENTATION_RULE: wrapVersioned<DeleteInstrumentationRuleResult, DeleteInstrumentationRuleVars>(DELETE_INSTURMENTATION_RULE, undefined, undefined, {
    select: (d) => d.DeleteInstrumentationRuleDocument,
    transformVariables: (vars) => vmDeleteInstrumentationRuleVariables(vars),
    transformResult: (data) => vmDeleteInstrumentationRuleResult(data as DeleteInstrumentationRuleMutation),
  }),

  // data streams
  GET_DATA_STREAMS: wrapVersioned<GetDataStreamsData, undefined>(GET_DATA_STREAMS),
  UPDATE_DATA_STREAM: wrapVersioned<UpdateDataStreamResult, UpdateDataStreamVars>(UPDATE_DATA_STREAM),
  DELETE_DATA_STREAM: wrapVersioned<DeleteDataStreamResult, DeleteDataStreamVars>(DELETE_DATA_STREAM),

  // namespaces
  //
  // Modern (>= v1.20): wire returns `{ namespaces: [...] }` flat at the root;
  // the kit's slot expects the same flat shape, so the pass-through `extract`
  // makes the bare typing explicit. Legacy (< v1.20): the v1.20+ `namespaces`
  // field doesn't exist, so resolve to a single combined query over
  // `k8sActualNamespaces { ... sources }` mapped into the modern `Namespace`
  // shape (replaces the old all-namespaces + per-namespace N+1 fetch).
  GET_NAMESPACES_WITH_WORKLOADS: wrapVersioned<GetNamespacesWithWorkloadsData, undefined>(
    GET_NAMESPACES_WITH_WORKLOADS,
    (d) => ({ namespaces: (d as { namespaces?: GetNamespacesWithWorkloadsData['namespaces'] })?.namespaces ?? [] }),
    {
      query: GET_NAMESPACES_WITH_SOURCES,
      map: (data) => legacyNamespacesToNamespaces(data),
    },
    // VM/connector: the "Add Sources" drawer is a discovery view, so resolve to
    // `workloadReports` (discoverable workloads), grouped by kind into the kit's
    // namespace+workload shape (the left column is relabeled "Kinds").
    // agents-api >= v0.16.0 adds `region` to WorkloadReport; the published
    // dialect snapshots through v0.15.0 omit it, so patch it in when the
    // connected schema supports it (connectors report v0.16.0).
    {
      select: (d) => d.GetWorkloadReportsDocument,
      patchQuery: (query, ctx) => (supportsWorkloadReportRegion(dialectVersion(ctx)) ? withWorkloadReportRegion(query) : query),
      transformResult: (data) => mapVmWorkloadReportsToNamespaces(data),
    },
  ),
  // VM has no namespace concept, so the bulk namespace persist is unsupported.
  PERSIST_NAMESPACES: wrapVersioned<PersistNamespacesResult, PersistNamespacesVars>(PERSIST_NAMESPACES, undefined, undefined, { unsupported: true }),

  // k8s manifest — wire field is `k8sManifest` (a top-level string); we
  // also accept the legacy `k8sActualManifest`/`manifest` names as
  // fallbacks for any older backend version still in flight.
  GET_K8S_MANIFEST: wrapVersioned<string, GetK8sManifestVars>(GET_K8S_MANIFEST, (d) => {
    const obj = d as { k8sManifest?: string; k8sActualManifest?: string; manifest?: string } | string | null | undefined;
    if (!obj) return undefined;
    if (typeof obj === 'string') return obj;
    return obj.k8sManifest ?? obj.k8sActualManifest ?? obj.manifest;
  }),

  // config
  GET_CONFIG: wrapVersioned<FetchedConfig, undefined>(GET_CONFIG, (d) => (d as { config?: FetchedConfig })?.config),
  // Wire returns `{ effectiveConfig: { … } }`; the kit's slot is the
  // bare `EffectiveConfig`. Walk the envelope here.
  GET_EFFECTIVE_CONFIG: wrapVersioned<EffectiveConfig, { id?: string }>(
    GET_EFFECTIVE_CONFIG,
    (d) =>
      (d as { effectiveConfig?: EffectiveConfig; computePlatform?: { effectiveConfig?: EffectiveConfig } })?.effectiveConfig ??
      (d as { computePlatform?: { effectiveConfig?: EffectiveConfig } })?.computePlatform?.effectiveConfig,
  ),
  GET_CONFIG_YAMLS: wrapVersioned<GetConfigYamlsData, undefined>(GET_CONFIG_YAMLS),
  UPDATE_REMOTE_CONFIG: withRemoteConfigVars(wrapVersioned<UpdateRemoteConfigResult, UpdateRemoteConfigVars>(UPDATE_REMOTE_CONFIG)),
  UPDATE_LOCAL_UI_CONFIG: wrapVersioned<UpdateLocalUiConfigResult, UpdateLocalUiConfigVars>(UPDATE_LOCAL_UI_CONFIG),
  RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS: wrapVersioned<ResetLocalUiConfigResult, undefined>(RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS),

  // describe
  GET_DESCRIBE_ODIGOS: wrapVersioned<DescribeOdigosData, undefined>(GET_DESCRIBE_ODIGOS),
  GET_DESCRIBE_SOURCE: wrapVersioned<DescribeSourceData, WorkloadId>(GET_DESCRIBE_SOURCE),

  // tokens — wire returns `{ computePlatform: { apiTokens: [...] } }`;
  // accept the legacy `tokens` name too for older backends.
  GET_TOKENS: wrapVersioned<TokenPayload[], undefined>(GET_TOKENS, (d) => {
    const env = d as
      | {
          apiTokens?: TokenPayload[];
          tokens?: TokenPayload[];
          computePlatform?: { apiTokens?: TokenPayload[]; tokens?: TokenPayload[] };
        }
      | null
      | undefined;
    return env?.computePlatform?.apiTokens ?? env?.computePlatform?.tokens ?? env?.apiTokens ?? env?.tokens ?? [];
  }),
  UPDATE_TOKEN: wrapVersioned<UpdateTokenResult, UpdateTokenVars>(UPDATE_TOKEN),

  // metrics / service map
  GET_METRICS: wrapVersioned<GetMetricsData, undefined>(GET_METRICS),
  // Wire returns `{ getServiceMap: { services: [...] } }`. Surface the
  // bare `services` array on `serviceMap` so the kit's container can
  // pluck `data.serviceMap` without walking the wire envelope.
  GET_SERVICE_MAP: wrapVersioned<GetServiceMapData, undefined>(GET_SERVICE_MAP, (d): GetServiceMapData => {
    const env = d as { getServiceMap?: { services?: ServiceMapSources }; serviceMap?: ServiceMapSources } | null | undefined;
    return { serviceMap: env?.getServiceMap?.services ?? env?.serviceMap ?? [] };
  }),

  // profiling — bare-shape slots, extract from `{ <field>: <bare> }` envelopes.
  GET_PROFILING_SLOTS: wrapVersioned<ProfilingSlots, undefined>(GET_PROFILING_SLOTS, (d) => (d as { profilingSlots?: ProfilingSlots })?.profilingSlots),
  // Wire returns `{ computePlatform: { source: { profiling: { profileJson } } } }`.
  // Walk the path and surface the bare `{ profileJson }` shape so the
  // consumer never has to narrow.
  GET_SOURCE_PROFILING: wrapVersioned<SourceProfilingResult, WorkloadId>(GET_SOURCE_PROFILING, (d) => {
    const env = d as { computePlatform?: { source?: { profiling?: SourceProfilingResult } }; sourceProfiling?: SourceProfilingResult } | null | undefined;
    return env?.computePlatform?.source?.profiling ?? env?.sourceProfiling;
  }),
  ENABLE_SOURCE_PROFILING: wrapVersioned<EnableProfilingResult, WorkloadId>(ENABLE_SOURCE_PROFILING, (d) => (d as { enableSourceProfiling?: EnableProfilingResult })?.enableSourceProfiling),

  // pipeline collectors — bare-shape slots. The backend's GraphQL
  // field names predate the kit's normalization: the gateway is
  // exposed as `gatewayDeploymentInfo`, the node-collector as
  // `odigletDaemonSetInfo` / `odigletPods`. Extract from the wire
  // field names here so consumers see the canonical kit slot shape.
  GET_GATEWAY_INFO: wrapVersioned<GatewayInfo, undefined>(GET_GATEWAY_INFO, (d) => (d as { gatewayDeploymentInfo?: GatewayInfo })?.gatewayDeploymentInfo),
  GET_GATEWAY_PODS: wrapVersioned<PodInfo[], undefined>(GET_GATEWAY_PODS, (d) => (d as { gatewayPods?: PodInfo[] })?.gatewayPods ?? []),
  GET_NODE_COLLECTOR_INFO: wrapVersioned<NodeCollectoInfo, undefined>(GET_NODE_COLLECTOR_INFO, (d) => (d as { odigletDaemonSetInfo?: NodeCollectoInfo })?.odigletDaemonSetInfo),
  GET_NODE_COLLECTOR_PODS: wrapVersioned<PodInfo[], undefined>(GET_NODE_COLLECTOR_PODS, (d) => (d as { odigletPods?: PodInfo[] })?.odigletPods ?? []),
  // Wire field is `collectorPod`. Legacy `extendedPodInfo` retained as
  // a fallback for older backend versions that might still ship it.
  GET_COLLECTOR_POD_INFO: wrapVersioned<ExtendedPodInfo, GetCollectorPodInfoVars>(GET_COLLECTOR_POD_INFO, (d) => {
    const env = d as { collectorPod?: ExtendedPodInfo; extendedPodInfo?: ExtendedPodInfo } | null | undefined;
    return env?.collectorPod ?? env?.extendedPodInfo;
  }),

  // sampling
  // Wire returns `{ sampling: { rules: [...], configs: { effective: {
  // k8sHealthProbesSampling: { enabled, keepPercentage } } } } }`.
  // Flatten to the kit's bare `{ samplingRules, k8sHealthProbesConfig }`
  // shape so consumers don't have to walk the nested envelope.
  GET_SAMPLING_RULES: wrapVersioned<GetSamplingRulesData, undefined>(GET_SAMPLING_RULES, (d): GetSamplingRulesData => {
    const env = d as
      | {
          sampling?: {
            rules?: SamplingRules[];
            configs?: { effective?: { k8sHealthProbesSampling?: SamplingRulesK8sHealthConfig } };
          };
        }
      | null
      | undefined;
    return {
      samplingRules: env?.sampling?.rules ?? [],
      k8sHealthProbesConfig: env?.sampling?.configs?.effective?.k8sHealthProbesSampling ?? undefined,
    };
  }),
  CREATE_NOISY_OPERATION_RULE: wrapVersioned<CreateNoisyOperationRuleResult, SamplingRuleCreateVars<NoisyOperationRuleInput>>(CREATE_NOISY_OPERATION_RULE),
  UPDATE_NOISY_OPERATION_RULE: wrapVersioned<UpdateNoisyOperationRuleResult, SamplingRuleUpdateVars<NoisyOperationRuleInput>>(UPDATE_NOISY_OPERATION_RULE),
  DELETE_NOISY_OPERATION_RULE: wrapVersioned<DeleteNoisyOperationRuleResult, SamplingRuleDeleteVars>(DELETE_NOISY_OPERATION_RULE),
  CREATE_HIGHLY_RELEVANT_OPERATION_RULE: wrapVersioned<CreateHighlyRelevantOperationRuleResult, SamplingRuleCreateVars<HighlyRelevantOperationRuleInput>>(CREATE_HIGHLY_RELEVANT_OPERATION_RULE),
  UPDATE_HIGHLY_RELEVANT_OPERATION_RULE: wrapVersioned<UpdateHighlyRelevantOperationRuleResult, SamplingRuleUpdateVars<HighlyRelevantOperationRuleInput>>(UPDATE_HIGHLY_RELEVANT_OPERATION_RULE),
  DELETE_HIGHLY_RELEVANT_OPERATION_RULE: wrapVersioned<DeleteHighlyRelevantOperationRuleResult, SamplingRuleDeleteVars>(DELETE_HIGHLY_RELEVANT_OPERATION_RULE),
  CREATE_COST_REDUCTION_RULE: wrapVersioned<CreateCostReductionRuleResult, SamplingRuleCreateVars<CostReductionRuleInput>>(CREATE_COST_REDUCTION_RULE),
  UPDATE_COST_REDUCTION_RULE: wrapVersioned<UpdateCostReductionRuleResult, SamplingRuleUpdateVars<CostReductionRuleInput>>(UPDATE_COST_REDUCTION_RULE),
  DELETE_COST_REDUCTION_RULE: wrapVersioned<DeleteCostReductionRuleResult, SamplingRuleDeleteVars>(DELETE_COST_REDUCTION_RULE),
  UPDATE_LOCAL_UI_SAMPLING_CONFIG: wrapVersioned<UpdateLocalUiSamplingConfigResult, UpdateLocalUiSamplingConfigVars>(UPDATE_LOCAL_UI_SAMPLING_CONFIG),

  // snapshots — central-only, direct (NOT versioned-remote-fetch). The
  // wire returns `{ allClusterSnapshots: … }` / `{ clusterSnapshot: … }`;
  // unwrap via `transformResult` so consumers receive the bare slot shape
  // and don't have to runtime-narrow.
  GET_ALL_CLUSTER_SNAPSHOTS: {
    document: GET_ALL_CLUSTER_SNAPSHOTS as DocumentNode,
    // @deprecated-legacy-api(v1.20) — the central backend can't build a
    // snapshot for a < v1.20 proxy (returns 0 results). Block the op for a
    // selected legacy proxy so the kit's AddSourceDrawer falls back to the
    // per-proxy `GET_NAMESPACES_WITH_WORKLOADS` path — which this adapter maps
    // to the legacy `k8sActualNamespaces { … sources }` combined query. This
    // mirrors the pre-v1.20 behavior (the drawer always used per-proxy
    // namespaces, never cluster snapshots).
    // Also block for VM: cluster snapshots are a k8s cross-cluster aggregate.
    // The kit already routes VM through `GET_NAMESPACES_WITH_WORKLOADS`, but
    // guard here too so a stale/k8s-defaulted context can never run it for a VM.
    canRun: (ctx) => !isAgentsApiPlatform(ctx) && !isLegacySelectedProxy(ctx),
    transformResult: (raw) => (raw as { allClusterSnapshots?: AllClusterSnapshots } | null | undefined)?.allClusterSnapshots,
  },
  GET_CLUSTER_SNAPSHOT: {
    document: GET_CLUSTER_SNAPSHOT as DocumentNode,
    // The kit's `useSnapshots()` resolves to THIS single-cluster slot whenever a
    // specific proxy is selected (in-proxy pages — e.g. the overview's
    // AddSourceDrawer), scoping the drawer to the selected cluster instead of
    // listing every connected cluster (PLAT-1255). Snapshots are keyed by
    // proxyID server-side (`clusterId === proxyID`, see central-backend's
    // websocket client), so the kit passes `{ clusterId: ctx.proxyID }`. Mirror
    // GET_ALL_CLUSTER_SNAPSHOTS' guard so a legacy (< v1.20) or VM proxy is
    // blocked here too and the kit falls back to the per-proxy
    // `GET_NAMESPACES_WITH_WORKLOADS` path rather than querying a snapshot the
    // backend can't build.
    canRun: (ctx) => !isAgentsApiPlatform(ctx) && !isLegacySelectedProxy(ctx),
    transformResult: (raw) => (raw as { clusterSnapshot?: ClusterSnapshot } | null | undefined)?.clusterSnapshot,
  },
  }),
);

type AdapterProps = PropsWithChildren;

const INITIAL_CONTEXT: OperationContext = {
  platformType: PlatformType.K8s,
  tier: Tier.Onprem,
  version: 'v0.0.0',
};

const isAuthErrorMessage = (message?: string) => {
  if (!message) return false;
  const lower = message.toLowerCase();
  return lower.includes('authentication required') || lower.includes('authentication expired') || lower.includes('http 401');
};

/**
 * Lifts `useConfig().isReadonly` (VIEWER role, or deployment-readonly for
 * non-admins) up into the adapter so it can be threaded into the
 * `OperationContext`. The kit's domain hooks short-circuit every mutation
 * when `ctx.isReadonly` is true, so this is what actually blocks viewers
 * from writing.
 *
 * It must live INSIDE `<OdigosApiProvider>` because `useConfig` reads the
 * effective config through `useOdigosApi()`. The underlying
 * `GET_EFFECTIVE_CONFIG` op is gated on a selected proxy, so on out-of-proxy
 * pages (sign-in, /access-management) no query fires and `isReadonly` falls
 * back to the pure role check.
 *
 * Named `ConfigSync` to mirror the equivalent component in the OSS webapp.
 * Also resolves the VM-specific context fields (`schemaVersion` + a
 * `graphqlSchemaVersion`-derived `platformType` fallback) from the proxy store.
 */
const ConfigSync: FC<{ onChange: (ctx: ProxyOperationContext) => void }> = ({ onChange }) => {
  const { config, isReadonly } = useConfig();
  // Read proxy state directly from the zustand store (no Apollo dependency).
  const { proxyID, platformType, odigosVersion, graphqlSchemaVersion } = useProxyStore();

  useEffect(() => {
    // `platformType` can be briefly unset (store rehydration / navigation
    // timing). VM proxies always report a non-null `graphqlSchemaVersion` while
    // k8s proxies never do, so use it as a deterministic fallback — otherwise the
    // kit's capabilities (`canFetchSnapshots = !isVm`) flip to k8s and the Add
    // Sources drawer wrongly fetches the cluster-wide snapshot for a VM.
    const resolvedPlatformType = platformType || config?.platformType || (graphqlSchemaVersion ? PlatformType.Vm : INITIAL_CONTEXT.platformType);
    // Two versions travel in the context, and they differ for VM proxies:
    //   - `version`       = the agent binary version (what the user sees as the
    //                       installed agent); always `odigosVersion`.
    //   - `schemaVersion` = the GraphQL dialect VM proxies report separately
    //                       (`graphqlSchemaVersion`); drives dialect/bucket
    //                       selection via `dialectVersion(ctx)`. Left undefined
    //                       for k8s, which keeps selecting buckets by `version`.
    //                       When a VM proxy reports no schema version (older
    //                       vm-agent), `dialectVersion` falls back to `version`.
    // VM agents and cloud connectors both report an agents-api schema version
    // that drives dialect selection; k8s never does (keeps selecting by `version`).
    const usesAgentsApiDialect = resolvedPlatformType === PlatformType.Vm || resolvedPlatformType === PlatformType.Connector;
    onChange({
      proxyID,
      platformType: resolvedPlatformType,
      tier: config?.tier || INITIAL_CONTEXT.tier,
      version: odigosVersion || config?.odigosVersion || INITIAL_CONTEXT.version,
      schemaVersion: usesAgentsApiDialect ? (graphqlSchemaVersion ?? undefined) : undefined,
      // Connectors serve the agents-api mutations (createSource / createDestination /
      // etc.) the same way VM agents do, so they inherit the user's normal role-based
      // readonly flag rather than being force-locked.
      isReadonly,
    });
  }, [proxyID, platformType, odigosVersion, graphqlSchemaVersion, config?.platformType, config?.tier, config?.odigosVersion, isReadonly, onChange]);

  return null;
};

const OdigosApiAdapter: FC<AdapterProps> = ({ children }) => {
  // Context is synced from `GET_CONFIG` + proxy store by <ConfigSync> (below,
  // inside the provider).
  const [syncedContext, setSyncedContext] = useState<ProxyOperationContext>(INITIAL_CONTEXT);
  // Stable callback so <ConfigSync>'s effect doesn't re-fire each render.
  const onContextChange = useCallback((ctx: ProxyOperationContext) => setSyncedContext(ctx), []);

  const apolloConfig = useMemo(() => {
    // WS link is constructed once and passed in — keeps `graphql-ws` out of the kit's
    // dep tree and lets us own the `splitLink` routing externally.
    const wsLink: ApolloLink = new GraphQLWsLink(createClient({ url: API.GRAPHQL_WS }));

    // Auth-error redirect link mirrors the legacy `apollo-client.ts` behavior.
    const errorRedirectLink = new ErrorLink(({ error }) => {
      if (CombinedGraphQLErrors.is(error)) {
        if (error.errors.some((e) => isAuthErrorMessage(e.message))) {
          try {
            useUserStore.getState().clearAuth();
          } catch {}
          if (typeof window !== 'undefined') window.location.assign(ROUTES.SIGN_IN);
        }
        return;
      }
      if (ServerError.is(error) && error.statusCode === 401) {
        try {
          useUserStore.getState().clearAuth();
        } catch {}
        if (typeof window !== 'undefined') window.location.assign(ROUTES.SIGN_IN);
        return;
      }
      if (isAuthErrorMessage(error.message)) {
        try {
          useUserStore.getState().clearAuth();
        } catch {}
        if (typeof window !== 'undefined') window.location.assign(ROUTES.SIGN_IN);
      }
    });

    return {
      httpUrl: API.GRAPHQL_HTTP,
      wsLink,
      credentials: 'same-origin' as const,
      authHeader: (): Record<string, string> => {
        const accessToken = useUserStore.getState().accessToken;
        return accessToken ? { authorization: `Bearer ${accessToken}` } : {};
      },
      onAuthError: () => {
        try {
          useUserStore.getState().clearAuth();
        } catch {}
        if (typeof window !== 'undefined') window.location.assign(ROUTES.SIGN_IN);
      },
      defaultFetchPolicies: {
        watchQuery: 'cache-and-network' as const,
        query: 'cache-first' as const,
        mutate: 'network-only' as const,
      },
      // `remoteFetchErrorLink` is first (closest to the app) so the inner
      // errors it lifts reach Apollo's QueryManager intact; `errorRedirectLink`
      // handles transport/auth failures.
      additionalLinks: [remoteFetchErrorLink, errorRedirectLink],
    };
  }, []);

  return (
    <OdigosApiProvider apolloConfig={apolloConfig} operations={operations} context={syncedContext}>
      <ConfigSync onChange={onContextChange} />
      {children}
    </OdigosApiProvider>
  );
};

// Out-of-proxy operations also live in this file as bare gql constants
// re-exported for any host code that needs them directly (e.g. signin
// page using SIGN_IN, or the connections page deleting a proxy).
export { GET_COMPUTE_PLATFORMS, UPDATE_COMPUTE_PLATFORM, DELETE_PROXY_CONNECTION };

export default OdigosApiAdapter;
