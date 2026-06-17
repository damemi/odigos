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
import { useUserStore, useProxyStore } from '@/store';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { safeJsonParse, trimVersion } from '@odigos/ui-kit/functions';
import { PlatformType, Tier, type Action } from '@odigos/ui-kit/types';
import { GraphQLError } from 'graphql';
import { ApolloLink, type ApolloClient, type DocumentNode } from '@apollo/client';
import type { VersionedRemoteFetch, WorkloadWithOdigosHealthStatus } from '@/types';
import {
  API,
  ROUTES,
  getVersionedQuery,
  // @deprecated-legacy-api(v1.20) - legacy <v1.20 shape mappers, used only by
  // the `wrapVersioned` legacy branches below. Remove with the legacy paths.
  mapLegacySourceToWorkload,
  addAgentInjectionStatusToSources,
  legacyNamespaceToNamespace,
  type LegacySource,
  type LegacyK8sActualNamespace,
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
  const v = trimVersion(ctx.version);
  if (!(v > 0)) return base;
  const strip = new Set(STRIP_INPUT_FIELDS.filter((f) => v < f.minVersion).map((f) => f.name));
  const mapScopes = v < SOURCES_SCOPES_MIN_VERSION;
  if (!strip.size && !mapScopes) return base;
  return transformDrift(base, strip, mapScopes) as Record<string, unknown>;
};

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
    variables: sanitizeVariablesForVersion(vars, ctx),
  }),
  transformResult: (raw): TData | undefined => {
    const remoteFetch = (raw as RemoteFetchResponse | null | undefined)?.remoteFetch;
    if (typeof remoteFetch !== 'string') return undefined;
    const parsed = safeJsonParse<RemoteParsedPayload<TData>>(remoteFetch, { data: undefined });
    // Inner GraphQL errors are nested at `data.errors`; never surface that
    // envelope as valid data (the `remoteFetchErrorLink` lifts these to a
    // top-level Apollo error so the kit toasts the failure).
    if (innerRemoteErrors(parsed).length) return undefined;
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
        variables: { proxyIDs, query: queryString, variables: sanitizeVariablesForVersion(vars, ctx) },
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
      // Per-proxy GraphQL errors are nested at `data.errors` inside each row's
      // stringified payload (the top-level `errors` sibling is typically empty);
      // treat their presence as a failed row rather than silently dropping data.
      const innerErrors = innerRemoteErrors(parsed);
      const errFromApi = innerErrors.length ? innerErrors.join(', ') : undefined;
      const data = parsed?.data && !innerErrors.length ? (extract ? extract(parsed.data) : (parsed.data as TData)) : undefined;
      return {
        proxyID: row.proxyID,
        success: row.success && !innerErrors.length,
        data,
        error: errFromApi ?? row.error,
      };
    });
  },
});

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

const isLegacyProxy = (ctx: OperationContext): boolean => trimVersion(ctx.version) < V120;

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
  forward(operation).map((response) => {
    const remoteFetch = (response.data as RemoteFetchResponse | null | undefined)?.remoteFetch;
    if (typeof remoteFetch !== 'string') return response;
    const parsed = safeJsonParse<RemoteParsedPayload<unknown>>(remoteFetch, { data: undefined });
    const messages = innerRemoteErrors(parsed);
    if (!messages.length) return response;
    return { ...response, errors: [...(response.errors ?? []), ...messages.map((message) => new GraphQLError(message))] };
  }),
);

// Execute a single REMOTE_FETCH-backed `Operation` imperatively (mirrors the
// kit runner's `transformVariables -> client.query -> transformResult` single
// call). Used by legacy `run` executors to perform their round-trips entirely
// host-side without re-entering the kit runner.
const runRemoteSingle = async <TData, TVars>(client: ApolloClient<unknown>, op: Operation<TData, TVars>, vars: TVars | undefined, ctx: OperationContext): Promise<{ data?: TData; error?: string }> => {
  const document = typeof op.document === 'function' ? op.document(ctx) : op.document;
  if (!document) return { error: 'Operation not supported in this context' };
  const variables = op.transformVariables ? op.transformVariables(vars as TVars, ctx) : (vars as Record<string, unknown> | undefined);
  const result = await client.query({ query: document, variables, fetchPolicy: 'network-only', errorPolicy: 'all' });
  const data = op.transformResult ? op.transformResult(result.data, ctx) : (result.data as TData);
  const error = result.error ? result.error.message : result.errors?.length ? result.errors.map((e) => e.message).join(', ') : undefined;
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
  run?: (client: ApolloClient<unknown>, vars: TVars | undefined, ctx: OperationContext) => Promise<{ data?: TData; error?: string }>;
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
const wrapVersioned = <TData, TVars>(modern: VersionedRemoteFetch, extract?: (envelope: unknown) => TData | undefined, legacy?: LegacySpec<TData, TVars>): Operation<TData, TVars> => {
  const base = wrapRemote<TData, TVars>(modern, extract);
  if (!legacy) return base;

  const op: Operation<TData, TVars> = {
    ...base,
    canRun: (ctx) => {
      if (!resolveProxyID(ctx)) return false;
      if (isLegacyProxy(ctx)) {
        // `run` executors resolve their own per-step queries; single-query
        // legacy needs its combined query string to exist for this version.
        if (legacy.run) return true;
        return !!legacy.query && !!getVersionedQuery(legacy.query, ctx.platformType, ctx.version);
      }
      return !!getVersionedQuery(modern, ctx.platformType, ctx.version);
    },
  };

  if (legacy.query && legacy.map) {
    op.transformVariables = (vars, ctx) => {
      if (isLegacyProxy(ctx)) {
        return {
          proxyID: resolveProxyID(ctx),
          query: getVersionedQuery(legacy.query!, ctx.platformType, ctx.version),
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

// @deprecated-legacy-api(v1.20) — legacy source-by-id executor: for each id
// fetch `GET_SOURCE` (containers + conditions + health), then enrich
// agent-injection from a single legacy `GET_WORKLOADS` (K8s only).
const runLegacySourcesByIds = async (client: ApolloClient<unknown>, vars: GetWorkloadsByIdsVars | undefined, ctx: OperationContext): Promise<{ data?: GetWorkloadsByIdsData; error?: string }> => {
  const ids = vars?.ids ?? [];
  if (!ids.length) return { data: { workloadsByIds: [] } };

  const sourceOp = wrapRemote<{ computePlatform?: { source?: LegacySource } }, { sourceId: WorkloadId }>(GET_SOURCE);
  const fetched: Workload[] = [];
  for (const id of ids) {
    const res = await runRemoteSingle(client, sourceOp, { sourceId: id }, ctx);
    const src = res.data?.computePlatform?.source;
    if (src) fetched.push(mapLegacySourceToWorkload(src));
  }

  if (ctx.platformType === PlatformType.K8s && fetched.length) {
    const workloadsOp = wrapRemote<{ workloads?: WorkloadWithOdigosHealthStatus[] }, GetWorkloadsVars>(GET_WORKLOADS);
    const wlRes = await runRemoteSingle(client, workloadsOp, { filter: { markedForInstrumentation: true } }, ctx);
    return { data: { workloadsByIds: mergeAgentInjection(fetched, wlRes.data?.workloads) } };
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
  //
  // Legacy < v1.20: the kit always asks for `GET_WORKLOADS`; here it resolves
  // to a single combined query (`computePlatform.sources` + `workloads`
  // injection rows) mapped into `Workload`s. By-id resolves via a multi-step
  // `run` (one `GET_SOURCE` per id + a single agent-injection enrichment).
  GET_WORKLOADS: wrapVersioned<GetWorkloadsData, GetWorkloadsVars | undefined>(GET_WORKLOADS, undefined, {
    query: GET_SOURCES_WITH_STATUS,
    map: (data) => legacySourcesToWorkloads(data),
  }),
  GET_WORKLOADS_BY_IDS: wrapVersioned<GetWorkloadsByIdsData, GetWorkloadsByIdsVars>(GET_WORKLOADS_BY_IDS, undefined, {
    run: (client, vars, ctx) => runLegacySourcesByIds(client, vars, ctx),
  }),
  GET_WORKLOADS_BY_IDS_SLIM: wrapVersioned<GetWorkloadsByIdsData, GetWorkloadsByIdsVars>(GET_WORKLOADS_BY_IDS_SLIM, undefined, {
    run: (client, vars, ctx) => runLegacySourcesByIds(client, vars, ctx),
  }),
  GET_SOURCE: wrapVersioned<{ source?: Workload }, WorkloadId>(GET_SOURCE),
  GET_SOURCE_LIBRARIES: wrapVersioned<GetSourceLibrariesData, GetSourceLibrariesVars>(GET_SOURCE_LIBRARIES),
  GET_PEER_SOURCES: wrapVersioned<{ peerSources?: PeerSources }, GetPeerSourcesVars>(GET_PEER_SOURCES),
  PERSIST_SOURCES: wrapVersioned<PersistSourcesResult, PersistSourcesVars>(PERSIST_SOURCES),
  UPDATE_SOURCE: wrapVersioned<{ updateK8sActualSource: boolean }, UpdateSourceVars>(UPDATE_SOURCE),
  RESTART_WORKLOADS: wrapVersioned<{ restartWorkloads: boolean }, RestartWorkloadsVars>(RESTART_WORKLOADS),
  RESTART_POD: wrapVersioned<{ restartPod: boolean }, RestartPodVars>(RESTART_POD),
  RECOVER_FROM_ROLLBACK: wrapVersioned<{ recoverFromRollbackForWorkload: boolean }, RecoverFromRollbackVars>(RECOVER_FROM_ROLLBACK),

  // destinations
  GET_DESTINATIONS: wrapVersioned<GetDestinationsData, undefined>(GET_DESTINATIONS),
  // Kit slot types are `Operation<GetDestinationCategoriesResult, undefined>`
  // and `Operation<GetPotentialDestinationsResult, undefined>` — bind
  // them explicitly here so the kit consumer never sees `unknown`.
  GET_DESTINATION_CATEGORIES: wrapVersioned<GetDestinationCategoriesResult, undefined>(GET_DESTINATION_CATEGORIES),
  GET_POTENTIAL_DESTINATIONS: wrapVersioned<GetPotentialDestinationsResult, undefined>(GET_POTENTIAL_DESTINATIONS),
  CREATE_DESTINATION: wrapVersioned<CreateDestinationResult, CreateDestinationVars>(CREATE_DESTINATION),
  UPDATE_DESTINATION: wrapVersioned<UpdateDestinationData, UpdateDestinationVars>(UPDATE_DESTINATION),
  DELETE_DESTINATION: wrapVersioned<DeleteDestinationResult, DeleteDestinationVars>(DELETE_DESTINATION),
  TEST_DESTINATION_CONNECTION: {
    ...wrapVersioned<TestConnectionResponse, TestDestinationConnectionVars>(TEST_DESTINATION_CONNECTION),
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
  GET_ACTIONS: withActionRenames(wrapVersioned<GetActionsData, undefined>(GET_ACTIONS)),
  CREATE_ACTION: withActionRenames(wrapVersioned<CreateActionResult, CreateActionVars>(CREATE_ACTION)),
  UPDATE_ACTION: withActionRenames(wrapVersioned<UpdateActionResult, UpdateActionVars>(UPDATE_ACTION)),
  DELETE_ACTION: wrapVersioned<DeleteActionResult, DeleteActionVars>(DELETE_ACTION),

  // instrumentation rules
  GET_INSTRUMENTATION_RULES: wrapVersioned<GetInstrumentationRulesData, undefined>(GET_INSTRUMENTATION_RULES),
  CREATE_INSTRUMENTATION_RULE: wrapVersioned<CreateInstrumentationRuleResult, CreateInstrumentationRuleVars>(CREATE_INSTRUMENTATION_RULE),
  UPDATE_INSTRUMENTATION_RULE: wrapVersioned<UpdateInstrumentationRuleResult, UpdateInstrumentationRuleVars>(UPDATE_INSTURMENTATION_RULE),
  DELETE_INSTRUMENTATION_RULE: wrapVersioned<DeleteInstrumentationRuleResult, DeleteInstrumentationRuleVars>(DELETE_INSTURMENTATION_RULE),

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
  ),
  PERSIST_NAMESPACES: wrapVersioned<PersistNamespacesResult, PersistNamespacesVars>(PERSIST_NAMESPACES),

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
  UPDATE_REMOTE_CONFIG: wrapVersioned<UpdateRemoteConfigResult, UpdateRemoteConfigVars>(UPDATE_REMOTE_CONFIG),
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
    canRun: (ctx) => !isLegacySelectedProxy(ctx),
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
      // `remoteFetchErrorLink` is first (closest to the app) so the inner
      // errors it lifts reach Apollo's QueryManager intact; `errorRedirectLink`
      // handles transport/auth failures.
      additionalLinks: [remoteFetchErrorLink, errorRedirectLink],
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
