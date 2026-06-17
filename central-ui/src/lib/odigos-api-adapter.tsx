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
 *     `@odigos/ui-kit/contexts/odigos-api` — drop-in replacement for the
 *     `if (ctx.platformType === PlatformType.Vm) … else …` switches that
 *     today live inside individual `transformVariables` callbacks.
 *   - `versionedDocument(map)` and `vmDialectMap(dialects, select)` — when
 *     migrating off the central-ui-native `VersionedRemoteFetch` strings
 *     toward the vmagent codegen `dialects` registry, use these to feed
 *     printed `DocumentNode`s straight into `Operation.document(ctx)`. The
 *     kit will pick the right dialect bucket per `ctx.version` using the
 *     same floor-to-bucket rule as `getVersionedQuery`.
 */

import React, { type FC, type PropsWithChildren, useMemo } from 'react';
import { createClient } from 'graphql-ws';
import { onError } from '@apollo/client/link/error';
import type { VersionedRemoteFetch } from '@/types';
import { useUserStore, useProxyStore } from '@/store';
import { API, ROUTES, getVersionedQuery } from '@/utils';
import { safeJsonParse } from '@odigos/ui-kit/functions';
import { ApolloLink, type DocumentNode } from '@apollo/client';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { PlatformType, Tier, type Action } from '@odigos/ui-kit/types';
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
  type GetNamespaceVars,
  type GetNamespacesWithWorkloadsData,
  type GetPeerSourcesVars,
  type GetSamplingRulesData,
  type GetServiceMapData,
  type GetSourceConditionsData,
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
} from '@odigos/ui-kit/contexts/odigos-api';
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
  Namespace,
  NodeCollectoInfo,
  NoisyOperationRuleInput,
  PeerSources,
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
  GET_CONFIG,
  GET_CONFIG_YAMLS,
  GET_DATA_STREAMS,
  GET_DESCRIBE_ODIGOS,
  GET_DESCRIBE_SOURCE,
  GET_DESTINATIONS,
  GET_DESTINATION_CATEGORIES,
  GET_EFFECTIVE_CONFIG,
  GET_INSTRUMENTATION_RULES,
  GET_K8S_MANIFEST,
  GET_METRICS,
  GET_NAMESPACES,
  GET_NAMESPACES_WITH_WORKLOADS,
  GET_NAMESPACE,
  GET_GATEWAY_INFO,
  GET_GATEWAY_PODS,
  GET_NODE_COLLECTOR_INFO,
  GET_NODE_COLLECTOR_PODS,
  GET_COLLECTOR_POD_INFO,
  GET_PROFILING_SLOTS,
  GET_SOURCE_PROFILING,
  GET_SAMPLING_RULES,
  GET_SERVICE_MAP,
  GET_SOURCES,
  GET_SOURCE,
  GET_SOURCE_CONDITIONS,
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

// Generic `<TData, TVars>` lets each call site declare the exact
// server-side response and request shapes for that operation, so
// every kit slot's `Operation<TData, TVars>` lines up. We deliberately
// avoid `= unknown` defaults here — every call site MUST specify
// both, which keeps the slot type strictly typed end-to-end (any
// missing-generic call site becomes a TypeScript error rather than
// silently degrading to `unknown` data flowing through the kit).
const wrapRemote = <TData, TVars>(versioned: VersionedRemoteFetch, extract?: (envelope: unknown) => TData | undefined): Operation<TData, TVars> => ({
  document: REMOTE_FETCH,
  // Every REMOTE_FETCH op needs a target proxy: either the currently
  // selected one (`ctx.proxyID`, in-proxy pages) OR a connections-scope
  // selection (`ctx.connectionIds`, the bulk drawers + EditConfigDrawer on
  // `/connections`). On out-of-proxy pages with NEITHER (sign-in,
  // `/access-management`, the bare connections list before any selection)
  // `ctx.proxyID` is undefined and `getVersionedQuery` has no version to
  // resolve, so dispatching would send an empty
  // `RemoteFetch($proxyID, $query, $variables)` with both required vars
  // missing — the backend rejects it, and if the store is reset mid-flight
  // (proxy navigation) Apollo throws "Store reset while query was in
  // flight". The kit honors this guard by skipping the query entirely
  // (no network request, no observer).
  canRun: (ctx) => !!resolveProxyID(ctx) && !!getVersionedQuery(versioned, ctx.platformType, ctx.version),
  transformVariables: (vars, ctx) => ({
    // In connections scope there's no single "selected" proxy — the bulk
    // drawers pass `connectionIds`. We use the first as the coordinator
    // proxy the REMOTE_FETCH is addressed to; ops that genuinely fan out
    // server-side (e.g. `updateRemoteConfig`) also receive the full
    // `connectionIds` list inside their own `variables`.
    proxyID: resolveProxyID(ctx),
    query: getVersionedQuery(versioned, ctx.platformType, ctx.version),
    variables: vars ?? {},
  }),
  transformResult: (raw): TData | undefined => {
    const remoteFetch = (raw as RemoteFetchResponse | null | undefined)?.remoteFetch;
    if (typeof remoteFetch !== 'string') return undefined;
    const parsed = safeJsonParse<RemoteParsedPayload<TData>>(remoteFetch, { data: undefined });
    if (!parsed?.data) return undefined;
    return extract ? extract(parsed.data) : (parsed.data as TData);
  },
  documentMulti: REMOTE_MULTI_FETCH,
  // Group all proxies under the active context's version. The legacy
  // `useRemoteFetch.fetchMulti` supported mixed-version fan-out by reading
  // each proxy's reported version from the connections list and grouping
  // accordingly; that subtlety is preserved as a follow-up so the kit's
  // runner stays simple. For now the fan-out uses ctx.version + ctx.platformType,
  // which matches today's behavior for in-proxy CRUD calls.
  transformVariablesMulti: (proxyIDs, vars, ctx): MultiFetchGroup[] => {
    const queryString = getVersionedQuery(versioned, ctx.platformType, ctx.version);
    if (!queryString) return [];

    return [
      {
        document: REMOTE_MULTI_FETCH,
        variables: { proxyIDs, query: queryString, variables: vars ?? {} },
        proxyIDs,
      },
    ];
  },
  // Map the REMOTE_MULTI_FETCH response array of `{ proxyID, success, data, error }`
  // into the kit's `MultiFetchResult[]`, parsing the stringified JSON `data` per row.
  transformMultiResult: (raw): MultiFetchResult<TData>[] => {
    const rows = (raw as RemoteMultiFetchResponse | null | undefined)?.remoteMultiFetch ?? [];
    return rows.map((row) => {
      const parsed = row.data ? safeJsonParse<RemoteParsedPayload<TData>>(row.data, {}) : undefined;
      const errFromApi = parsed?.errors?.length ? parsed.errors.map((e) => e.message).join(', ') : undefined;
      const data = parsed?.data ? (extract ? extract(parsed.data) : (parsed.data as TData)) : undefined;
      return {
        proxyID: row.proxyID,
        success: row.success,
        data,
        error: errFromApi ?? row.error,
      };
    });
  },
});

// The backend's `ActionFieldsInput.renames` is a JSON-stringified `String`
// (it `json.Unmarshal`s the value server-side), while the kit models
// `renames` as an object map. Serialize on the way out (create/update) and
// parse on the way back (list) so the kit's RenameAttribute form always sees
// the object shape. This (de)serialization previously lived in the per-app
// action hook, retired in favor of the kit's `useActionsApi`; without it the
// raw object is sent to a `String` scalar and the mutation is rejected (no
// CRD is created). `withActionRenames` composes this around `wrapRemote` so it
// survives the REMOTE_FETCH round-trip.
const parseRenamesString = (value: string): Record<string, string> => {
  const parsed = safeJsonParse<unknown>(value, {});
  return parsed && typeof parsed === 'object' ? (parsed as Record<string, string>) : {};
};

const serializeActionRenames = (vars: unknown): unknown => {
  const v = vars as { action?: { fields?: { renames?: Record<string, string> | null } } } | undefined;
  const action = v?.action;
  if (!action) return vars;
  const renames = action.fields?.renames;
  return {
    ...v,
    action: {
      ...action,
      fields: {
        ...action.fields,
        // Mirror the legacy hook: an empty/missing map is sent as null so the
        // controller skips the RenameAttribute config entirely.
        renames: renames && Object.keys(renames).length ? JSON.stringify(renames) : null,
      },
    },
  };
};

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

// Wrap a REMOTE_FETCH-backed action op so `renames` is stringified before the
// inner variables are embedded, and parsed back out of the inner response.
const withActionRenames = <TData, TVars>(op: Operation<TData, TVars>): Operation<TData, TVars> => ({
  ...op,
  transformVariables: op.transformVariables ? (vars, ctx) => op.transformVariables!(serializeActionRenames(vars) as TVars, ctx) : undefined,
  transformResult: (raw, ctx) => parseActionRenamesInData(op.transformResult ? op.transformResult(raw, ctx) : (raw as TData | undefined)),
});

const operations: OdigosApiOperations = {
  // sources / workloads
  GET_WORKLOADS: wrapRemote<GetWorkloadsData, GetWorkloadsVars | undefined>(GET_WORKLOADS),
  GET_WORKLOADS_BY_IDS: wrapRemote<GetWorkloadsByIdsData, GetWorkloadsByIdsVars>(GET_WORKLOADS_BY_IDS),
  GET_WORKLOADS_BY_IDS_SLIM: wrapRemote<GetWorkloadsByIdsData, GetWorkloadsByIdsVars>(GET_WORKLOADS_BY_IDS_SLIM),
  GET_SOURCE: wrapRemote<{ source?: Workload }, WorkloadId>(GET_SOURCE),
  GET_SOURCES: wrapRemote<GetWorkloadsData, GetWorkloadsVars | undefined>(GET_SOURCES),
  GET_SOURCE_CONDITIONS: wrapRemote<GetSourceConditionsData, WorkloadId>(GET_SOURCE_CONDITIONS),
  GET_SOURCE_LIBRARIES: wrapRemote<GetSourceLibrariesData, GetSourceLibrariesVars>(GET_SOURCE_LIBRARIES),
  GET_PEER_SOURCES: wrapRemote<{ peerSources?: PeerSources }, GetPeerSourcesVars>(GET_PEER_SOURCES),
  PERSIST_SOURCES: wrapRemote<PersistSourcesResult, PersistSourcesVars>(PERSIST_SOURCES),
  UPDATE_SOURCE: wrapRemote<{ updateK8sActualSource: boolean }, UpdateSourceVars>(UPDATE_SOURCE),
  RESTART_WORKLOADS: wrapRemote<{ restartWorkloads: boolean }, RestartWorkloadsVars>(RESTART_WORKLOADS),
  RESTART_POD: wrapRemote<{ restartPod: boolean }, RestartPodVars>(RESTART_POD),
  RECOVER_FROM_ROLLBACK: wrapRemote<{ recoverFromRollbackForWorkload: boolean }, RecoverFromRollbackVars>(RECOVER_FROM_ROLLBACK),

  // destinations
  GET_DESTINATIONS: wrapRemote<GetDestinationsData, undefined>(GET_DESTINATIONS),
  // Kit slot types are `Operation<GetDestinationCategoriesResult, undefined>`
  // and `Operation<GetPotentialDestinationsResult, undefined>` — bind
  // them explicitly here so the kit consumer never sees `unknown`.
  GET_DESTINATION_CATEGORIES: wrapRemote<GetDestinationCategoriesResult, undefined>(GET_DESTINATION_CATEGORIES),
  GET_POTENTIAL_DESTINATIONS: wrapRemote<GetPotentialDestinationsResult, undefined>(GET_POTENTIAL_DESTINATIONS),
  CREATE_DESTINATION: wrapRemote<CreateDestinationResult, CreateDestinationVars>(CREATE_DESTINATION),
  UPDATE_DESTINATION: wrapRemote<UpdateDestinationData, UpdateDestinationVars>(UPDATE_DESTINATION),
  DELETE_DESTINATION: wrapRemote<DeleteDestinationResult, DeleteDestinationVars>(DELETE_DESTINATION),
  TEST_DESTINATION_CONNECTION: {
    ...wrapRemote<TestConnectionResponse, TestDestinationConnectionVars>(TEST_DESTINATION_CONNECTION),
    // The test-connection mutation's GraphQL shape returns the result
    // under `testConnectionForDestination`, not the wrapper's default
    // `data` field. Override `transformResult` to dig that one extra level.
    transformResult: (raw): TestConnectionResponse | undefined => {
      const remoteFetch = (raw as RemoteFetchResponse | null | undefined)?.remoteFetch;
      if (typeof remoteFetch !== 'string') return undefined;
      const parsed = safeJsonParse<{ data?: { testConnectionForDestination?: TestConnectionResponse } }>(remoteFetch, { data: undefined });
      return parsed?.data?.testConnectionForDestination;
    },
  },

  // actions. `renames` is a JSON-string on the wire; (de)serialize it so the
  // kit's object-shaped `renames` survives the proxy round-trip (see helpers above).
  GET_ACTIONS: withActionRenames(wrapRemote<GetActionsData, undefined>(GET_ACTIONS)),
  CREATE_ACTION: withActionRenames(wrapRemote<CreateActionResult, CreateActionVars>(CREATE_ACTION)),
  UPDATE_ACTION: withActionRenames(wrapRemote<UpdateActionResult, UpdateActionVars>(UPDATE_ACTION)),
  DELETE_ACTION: wrapRemote<DeleteActionResult, DeleteActionVars>(DELETE_ACTION),

  // instrumentation rules
  GET_INSTRUMENTATION_RULES: wrapRemote<GetInstrumentationRulesData, undefined>(GET_INSTRUMENTATION_RULES),
  CREATE_INSTRUMENTATION_RULE: wrapRemote<CreateInstrumentationRuleResult, CreateInstrumentationRuleVars>(CREATE_INSTRUMENTATION_RULE),
  UPDATE_INSTRUMENTATION_RULE: wrapRemote<UpdateInstrumentationRuleResult, UpdateInstrumentationRuleVars>(UPDATE_INSTURMENTATION_RULE),
  DELETE_INSTRUMENTATION_RULE: wrapRemote<DeleteInstrumentationRuleResult, DeleteInstrumentationRuleVars>(DELETE_INSTURMENTATION_RULE),

  // data streams
  GET_DATA_STREAMS: wrapRemote<GetDataStreamsData, undefined>(GET_DATA_STREAMS),
  UPDATE_DATA_STREAM: wrapRemote<UpdateDataStreamResult, UpdateDataStreamVars>(UPDATE_DATA_STREAM),
  DELETE_DATA_STREAM: wrapRemote<DeleteDataStreamResult, DeleteDataStreamVars>(DELETE_DATA_STREAM),

  // namespaces (keeps legacy < v1.20 paths exposed; the kit ignores the
  // optional ones unless the consumer hook reaches for them).
  //
  // The wire returns `{ namespaces: [...] }` flat at the root for v1.20+;
  // the kit's slot expects the same flat shape. Pass-through `extract`
  // makes the bare typing explicit and survives any future wire-shape
  // drift behind the same kit slot.
  GET_NAMESPACES_WITH_WORKLOADS: wrapRemote<GetNamespacesWithWorkloadsData, undefined>(GET_NAMESPACES_WITH_WORKLOADS, (d) => ({
    namespaces: (d as { namespaces?: GetNamespacesWithWorkloadsData['namespaces'] })?.namespaces ?? [],
  })),
  GET_NAMESPACES: wrapRemote<GetNamespacesWithWorkloadsData, undefined>(GET_NAMESPACES),
  GET_NAMESPACE: wrapRemote<{ namespace?: Namespace }, GetNamespaceVars>(GET_NAMESPACE),
  PERSIST_NAMESPACES: wrapRemote<PersistNamespacesResult, PersistNamespacesVars>(PERSIST_NAMESPACES),

  // k8s manifest — wire field is `k8sManifest` (a top-level string); we
  // also accept the legacy `k8sActualManifest`/`manifest` names as
  // fallbacks for any older backend version still in flight.
  GET_K8S_MANIFEST: wrapRemote<string, GetK8sManifestVars>(GET_K8S_MANIFEST, (d) => {
    const obj = d as { k8sManifest?: string; k8sActualManifest?: string; manifest?: string } | string | null | undefined;
    if (!obj) return undefined;
    if (typeof obj === 'string') return obj;
    return obj.k8sManifest ?? obj.k8sActualManifest ?? obj.manifest;
  }),

  // config
  GET_CONFIG: wrapRemote<FetchedConfig, undefined>(GET_CONFIG, (d) => (d as { config?: FetchedConfig })?.config),
  // Wire returns `{ effectiveConfig: { … } }`; the kit's slot is the
  // bare `EffectiveConfig`. Walk the envelope here.
  GET_EFFECTIVE_CONFIG: wrapRemote<EffectiveConfig, { id?: string }>(
    GET_EFFECTIVE_CONFIG,
    (d) =>
      (d as { effectiveConfig?: EffectiveConfig; computePlatform?: { effectiveConfig?: EffectiveConfig } })?.effectiveConfig ??
      (d as { computePlatform?: { effectiveConfig?: EffectiveConfig } })?.computePlatform?.effectiveConfig,
  ),
  GET_CONFIG_YAMLS: wrapRemote<GetConfigYamlsData, undefined>(GET_CONFIG_YAMLS),
  UPDATE_REMOTE_CONFIG: wrapRemote<UpdateRemoteConfigResult, UpdateRemoteConfigVars>(UPDATE_REMOTE_CONFIG),
  UPDATE_LOCAL_UI_CONFIG: wrapRemote<UpdateLocalUiConfigResult, UpdateLocalUiConfigVars>(UPDATE_LOCAL_UI_CONFIG),
  RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS: wrapRemote<ResetLocalUiConfigResult, undefined>(RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS),

  // describe
  GET_DESCRIBE_ODIGOS: wrapRemote<DescribeOdigosData, undefined>(GET_DESCRIBE_ODIGOS),
  GET_DESCRIBE_SOURCE: wrapRemote<DescribeSourceData, WorkloadId>(GET_DESCRIBE_SOURCE),

  // tokens — wire returns `{ computePlatform: { apiTokens: [...] } }`;
  // accept the legacy `tokens` name too for older backends.
  GET_TOKENS: wrapRemote<TokenPayload[], undefined>(GET_TOKENS, (d) => {
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
  UPDATE_TOKEN: wrapRemote<UpdateTokenResult, UpdateTokenVars>(UPDATE_TOKEN),

  // metrics / service map
  GET_METRICS: wrapRemote<GetMetricsData, undefined>(GET_METRICS),
  // Wire returns `{ getServiceMap: { services: [...] } }`. Surface the
  // bare `services` array on `serviceMap` so the kit's container can
  // pluck `data.serviceMap` without walking the wire envelope.
  GET_SERVICE_MAP: wrapRemote<GetServiceMapData, undefined>(GET_SERVICE_MAP, (d): GetServiceMapData => {
    const env = d as { getServiceMap?: { services?: ServiceMapSources }; serviceMap?: ServiceMapSources } | null | undefined;
    return { serviceMap: env?.getServiceMap?.services ?? env?.serviceMap ?? [] };
  }),

  // profiling — bare-shape slots, extract from `{ <field>: <bare> }` envelopes.
  GET_PROFILING_SLOTS: wrapRemote<ProfilingSlots, undefined>(GET_PROFILING_SLOTS, (d) => (d as { profilingSlots?: ProfilingSlots })?.profilingSlots),
  // Wire returns `{ computePlatform: { source: { profiling: { profileJson } } } }`.
  // Walk the path and surface the bare `{ profileJson }` shape so the
  // consumer never has to narrow.
  GET_SOURCE_PROFILING: wrapRemote<SourceProfilingResult, WorkloadId>(GET_SOURCE_PROFILING, (d) => {
    const env = d as { computePlatform?: { source?: { profiling?: SourceProfilingResult } }; sourceProfiling?: SourceProfilingResult } | null | undefined;
    return env?.computePlatform?.source?.profiling ?? env?.sourceProfiling;
  }),
  ENABLE_SOURCE_PROFILING: wrapRemote<EnableProfilingResult, WorkloadId>(ENABLE_SOURCE_PROFILING, (d) => (d as { enableSourceProfiling?: EnableProfilingResult })?.enableSourceProfiling),

  // pipeline collectors — bare-shape slots. The backend's GraphQL
  // field names predate the kit's normalization: the gateway is
  // exposed as `gatewayDeploymentInfo`, the node-collector as
  // `odigletDaemonSetInfo` / `odigletPods`. Extract from the wire
  // field names here so consumers see the canonical kit slot shape.
  GET_GATEWAY_INFO: wrapRemote<GatewayInfo, undefined>(GET_GATEWAY_INFO, (d) => (d as { gatewayDeploymentInfo?: GatewayInfo })?.gatewayDeploymentInfo),
  GET_GATEWAY_PODS: wrapRemote<PodInfo[], undefined>(GET_GATEWAY_PODS, (d) => (d as { gatewayPods?: PodInfo[] })?.gatewayPods ?? []),
  GET_NODE_COLLECTOR_INFO: wrapRemote<NodeCollectoInfo, undefined>(GET_NODE_COLLECTOR_INFO, (d) => (d as { odigletDaemonSetInfo?: NodeCollectoInfo })?.odigletDaemonSetInfo),
  GET_NODE_COLLECTOR_PODS: wrapRemote<PodInfo[], undefined>(GET_NODE_COLLECTOR_PODS, (d) => (d as { odigletPods?: PodInfo[] })?.odigletPods ?? []),
  // Wire field is `collectorPod`. Legacy `extendedPodInfo` retained as
  // a fallback for older backend versions that might still ship it.
  GET_COLLECTOR_POD_INFO: wrapRemote<ExtendedPodInfo, GetCollectorPodInfoVars>(GET_COLLECTOR_POD_INFO, (d) => {
    const env = d as { collectorPod?: ExtendedPodInfo; extendedPodInfo?: ExtendedPodInfo } | null | undefined;
    return env?.collectorPod ?? env?.extendedPodInfo;
  }),

  // sampling
  // Wire returns `{ sampling: { rules: [...], configs: { effective: {
  // k8sHealthProbesSampling: { enabled, keepPercentage } } } } }`.
  // Flatten to the kit's bare `{ samplingRules, k8sHealthProbesConfig }`
  // shape so consumers don't have to walk the nested envelope.
  GET_SAMPLING_RULES: wrapRemote<GetSamplingRulesData, undefined>(GET_SAMPLING_RULES, (d): GetSamplingRulesData => {
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
  CREATE_NOISY_OPERATION_RULE: wrapRemote<CreateNoisyOperationRuleResult, SamplingRuleCreateVars<NoisyOperationRuleInput>>(CREATE_NOISY_OPERATION_RULE),
  UPDATE_NOISY_OPERATION_RULE: wrapRemote<UpdateNoisyOperationRuleResult, SamplingRuleUpdateVars<NoisyOperationRuleInput>>(UPDATE_NOISY_OPERATION_RULE),
  DELETE_NOISY_OPERATION_RULE: wrapRemote<DeleteNoisyOperationRuleResult, SamplingRuleDeleteVars>(DELETE_NOISY_OPERATION_RULE),
  CREATE_HIGHLY_RELEVANT_OPERATION_RULE: wrapRemote<CreateHighlyRelevantOperationRuleResult, SamplingRuleCreateVars<HighlyRelevantOperationRuleInput>>(CREATE_HIGHLY_RELEVANT_OPERATION_RULE),
  UPDATE_HIGHLY_RELEVANT_OPERATION_RULE: wrapRemote<UpdateHighlyRelevantOperationRuleResult, SamplingRuleUpdateVars<HighlyRelevantOperationRuleInput>>(UPDATE_HIGHLY_RELEVANT_OPERATION_RULE),
  DELETE_HIGHLY_RELEVANT_OPERATION_RULE: wrapRemote<DeleteHighlyRelevantOperationRuleResult, SamplingRuleDeleteVars>(DELETE_HIGHLY_RELEVANT_OPERATION_RULE),
  CREATE_COST_REDUCTION_RULE: wrapRemote<CreateCostReductionRuleResult, SamplingRuleCreateVars<CostReductionRuleInput>>(CREATE_COST_REDUCTION_RULE),
  UPDATE_COST_REDUCTION_RULE: wrapRemote<UpdateCostReductionRuleResult, SamplingRuleUpdateVars<CostReductionRuleInput>>(UPDATE_COST_REDUCTION_RULE),
  DELETE_COST_REDUCTION_RULE: wrapRemote<DeleteCostReductionRuleResult, SamplingRuleDeleteVars>(DELETE_COST_REDUCTION_RULE),
  UPDATE_LOCAL_UI_SAMPLING_CONFIG: wrapRemote<UpdateLocalUiSamplingConfigResult, UpdateLocalUiSamplingConfigVars>(UPDATE_LOCAL_UI_SAMPLING_CONFIG),

  // snapshots — central-only, direct (NOT versioned-remote-fetch). The
  // wire returns `{ allClusterSnapshots: … }` / `{ clusterSnapshot: … }`;
  // unwrap via `transformResult` so consumers receive the bare slot shape
  // and don't have to runtime-narrow.
  GET_ALL_CLUSTER_SNAPSHOTS: {
    document: GET_ALL_CLUSTER_SNAPSHOTS as DocumentNode,
    transformResult: (raw) => (raw as { allClusterSnapshots?: AllClusterSnapshots } | null | undefined)?.allClusterSnapshots,
  },
  GET_CLUSTER_SNAPSHOT: {
    document: GET_CLUSTER_SNAPSHOT as DocumentNode,
    transformResult: (raw) => (raw as { clusterSnapshot?: ClusterSnapshot } | null | undefined)?.clusterSnapshot,
  },
};

interface AdapterProps extends PropsWithChildren {
  context?: OperationContext;
}

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

const OdigosApiAdapter: FC<AdapterProps> = ({ children, context }) => {
  // Read proxy state directly from the zustand store (no Apollo dependency).
  // This breaks the circular dependency where the adapter wants the active
  // proxy info to seed `OperationContext`, but `useProxy` itself depends on
  // Apollo (via `useComputePlatformsCRUD`) which is what the adapter mounts.
  const proxyID = useProxyStore((s) => s.proxyID);
  const platformType = useProxyStore((s) => s.platformType);
  const odigosVersion = useProxyStore((s) => s.odigosVersion);

  const resolvedContext = useMemo<OperationContext>(() => {
    if (context) return context;
    return {
      ...INITIAL_CONTEXT,
      proxyID,
      platformType: platformType ?? PlatformType.K8s,
      version: odigosVersion ?? 'v0.0.0',
    };
  }, [context, proxyID, platformType, odigosVersion]);

  const apolloConfig = useMemo(() => {
    // WS link is constructed once and passed in — keeps `graphql-ws` out of the kit's
    // dep tree and lets us own the `splitLink` routing externally.
    const wsLink: ApolloLink = new GraphQLWsLink(createClient({ url: API.GRAPHQL_WS }));

    // Auth-error redirect link mirrors the legacy `apollo-client.ts` behavior.
    const errorRedirectLink = onError(({ graphQLErrors, networkError }) => {
      if (graphQLErrors && graphQLErrors.length > 0) {
        const shouldRedirect = graphQLErrors.some((e) => isAuthErrorMessage(e.message));
        if (shouldRedirect) {
          try {
            useUserStore.getState().clearAuth();
          } catch {}
          if (typeof window !== 'undefined') window.location.assign(ROUTES.SIGN_IN);
        }
      }
      // Apollo's `networkError` is `Error | ServerError | ServerParseError | null`
      // — only `ServerError` exposes `statusCode`. Probe both fields
      // structurally rather than `as any`.
      if (networkError) {
        const probe = networkError as { statusCode?: number; message?: string };
        if (probe.statusCode === 401 || isAuthErrorMessage(probe.message)) {
          try {
            useUserStore.getState().clearAuth();
          } catch {}
          if (typeof window !== 'undefined') window.location.assign(ROUTES.SIGN_IN);
        }
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
      additionalLinks: [errorRedirectLink],
    };
  }, []);

  return (
    <OdigosApiProvider apolloConfig={apolloConfig} operations={operations} context={resolvedContext}>
      {children}
    </OdigosApiProvider>
  );
};

// Out-of-proxy operations also live in this file as bare gql constants
// re-exported for any host code that needs them directly (e.g. signin
// page using SIGN_IN, or the connections page deleting a proxy).
export { GET_COMPUTE_PLATFORMS, UPDATE_COMPUTE_PLATFORM, DELETE_PROXY_CONNECTION };

export default OdigosApiAdapter;
