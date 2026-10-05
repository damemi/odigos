import type { GetDestinationsQuery, GetDestinationTypesQuery } from '@odigos/vmagent-api-apollo-client';
import type {
  CreateDestinationResult,
  CreateDestinationVars,
  DeleteDestinationResult,
  DeleteDestinationVars,
  GetDestinationsData,
  OperationContext,
  TestDestinationConnectionVars,
  UpdateDestinationData,
  UpdateDestinationVars,
} from '@odigos/ui-kit/contexts';
import { type Destination, type DestinationTypes, type GetDestinationCategoriesResult, type TestConnectionResponse } from '@odigos/ui-kit/types';
import { dialectVersion } from '@/graphql/remote/vm-dialect';
import { supportsDestinationDisabled, withFragmentSelections } from '@/graphql/remote/utils';

// The agents-api destination surface (v0.12.0+) differs from the odigos k8s
// backend that central-ui was built against:
//   - the catalog is a flat `destinationTypes` list (no `destinationCategories`
//     grouping; `supportedSignals` are flat booleans).
//   - a configured destination is `{ name, type, config(JSON), signals, disabled }`
//     (no id / dataStreamNames / nested destinationType). `disabled` was added in
//     agents-api v0.24.0; older dialects omit it and we treat missing as false.
//   - mutations are per-destination create/update/delete + testConnection with
//     a `{ input }` argument and `TelemetrySignal` enum signals.
// These helpers translate between that surface and the kit's slot shapes.

const VM_DATA_STREAM = 'default';

// `TelemetrySignal[]` (TRACES|METRICS|LOGS|PROFILES) -> kit `exportedSignals`.
const signalsToExported = (signals: string[] | null | undefined): Destination['exportedSignals'] => ({
  traces: !!signals?.includes('TRACES'),
  metrics: !!signals?.includes('METRICS'),
  logs: !!signals?.includes('LOGS'),
  profiles: !!signals?.includes('PROFILES'),
});

// kit `exportedSignals` -> agents-api `signals` enum array.
const exportedToSignals = (s?: Partial<Destination['exportedSignals']>): string[] => {
  const out: string[] = [];
  if (s?.traces) out.push('TRACES');
  if (s?.metrics) out.push('METRICS');
  if (s?.logs) out.push('LOGS');
  if (s?.profiles) out.push('PROFILES');
  return out;
};

// form field rows -> agents-api `config` JSON map.
const fieldsToConfig = (fields?: { key: string; value: string }[]): Record<string, string> => {
  const cfg: Record<string, string> = {};
  for (const f of fields ?? []) cfg[f.key] = f.value;
  return cfg;
};

type VmDestinationEntity = {
  name: string;
  type: string;
  config?: Record<string, unknown> | null;
  signals?: string[] | null;
  disabled?: boolean | null;
};

// agents-api `Destination` -> kit `Destination`. VM carries no catalog metadata
// per configured destination, so `destinationType` is derived best-effort
// (displayName = type, supported = currently-exported signals).
const mapVmDestinationEntity = (d: VmDestinationEntity): Destination => {
  const exported = signalsToExported(d.signals);
  return {
    id: d.name,
    name: d.name,
    disabled: !!d.disabled,
    dataStreamNames: [VM_DATA_STREAM],
    exportedSignals: exported,
    fields: JSON.stringify(d.config ?? {}),
    conditions: null,
    destinationType: {
      type: d.type as DestinationTypes,
      displayName: d.type,
      supportedSignals: {
        logs: { supported: exported.logs },
        metrics: { supported: exported.metrics },
        traces: { supported: exported.traces },
        profiles: { supported: exported.profiles },
      },
    },
  };
};

// Category names recognized by the kit's add-destination drawer. The drawer
// (ui-kit add-destination-drawer/helpers.ts) buckets catalog items by matching
// `category.name` against these exact strings and DROPS anything else, so a
// category must be named one of these to render at all.
const SELF_HOSTED = 'self hosted';
const MANAGED = 'managed';

// agents-api v0.12.0+ dropped the managed/self-hosted grouping (it existed as
// `DestinationCategory.name` up to v0.11.0) — `destinationTypes` is now a flat
// list. This static `type -> category` map (sourced from odigos's destination
// `metadata.category` in destinations/data/*.yaml) re-derives the grouping
// client-side. Unknown/new types fall back to self-hosted so they still render.
const DESTINATION_CATEGORY: Record<string, string> = {
  alibabacloud: MANAGED,
  appdynamics: MANAGED,
  axiom: MANAGED,
  azureblob: MANAGED,
  azuremonitor: MANAGED,
  betterstack: MANAGED,
  bonree: MANAGED,
  causely: MANAGED,
  checkly: MANAGED,
  chronosphere: MANAGED,
  clickhouse: SELF_HOSTED,
  cloudwatch: MANAGED,
  coralogix: MANAGED,
  dash0: MANAGED,
  datadog: MANAGED,
  dynamic: SELF_HOSTED,
  dynatrace: MANAGED,
  elasticapm: MANAGED,
  elasticsearch: SELF_HOSTED,
  googlecloud: MANAGED,
  googlecloudotlp: MANAGED,
  grafanacloudloki: MANAGED,
  grafanacloudprometheus: MANAGED,
  grafanacloudtempo: MANAGED,
  greptime: MANAGED,
  groundcover: MANAGED,
  honeycomb: MANAGED,
  hyperdx: MANAGED,
  instana: MANAGED,
  jaeger: SELF_HOSTED,
  kafka: SELF_HOSTED,
  kloudmate: MANAGED,
  last9: MANAGED,
  lightstep: MANAGED,
  logzio: MANAGED,
  loki: SELF_HOSTED,
  lumigo: MANAGED,
  middleware: MANAGED,
  newrelic: MANAGED,
  observe: MANAGED,
  oneuptime: MANAGED,
  openobserve: MANAGED,
  oracle: MANAGED,
  otlp: SELF_HOSTED,
  otlphttp: SELF_HOSTED,
  prometheus: SELF_HOSTED,
  pyroscope: SELF_HOSTED,
  qryn: MANAGED,
  'qryn-oss': SELF_HOSTED,
  quickwit: SELF_HOSTED,
  s3: MANAGED,
  seq: SELF_HOSTED,
  signalfx: MANAGED,
  signoz: SELF_HOSTED,
  splunk: MANAGED,
  splunkotlp: MANAGED,
  sumologic: MANAGED,
  telemetryhub: MANAGED,
  tempo: SELF_HOSTED,
  tingyun: MANAGED,
  traceloop: MANAGED,
  uptrace: MANAGED,
  victoriametricscloud: MANAGED,
  xray: MANAGED,
};

type VmDestinationTypeItem = GetDestinationTypesQuery['destinationTypes'][number];

const toCatalogItem = (t: VmDestinationTypeItem) => ({
  type: t.type,
  displayName: t.displayName,
  imageUrl: t.imageUrl,
  testConnectionSupported: true,
  supportedSignals: {
    logs: { supported: t.supportedSignals.logs },
    metrics: { supported: t.supportedSignals.metrics },
    traces: { supported: t.supportedSignals.traces },
    profiles: { supported: t.supportedSignals.profiles },
  },
  fields: t.fields.map((f) => ({
    name: f.name,
    displayName: f.displayName,
    componentType: f.componentType,
    componentProperties: f.componentProperties,
    secret: f.secret,
    initialValue: f.initialValue ?? '',
    renderCondition: f.renderCondition ?? '',
    hideFromReadData: f.hideFromReadData,
    customReadDataLabels: f.customReadDataLabels,
  })),
});

// `GetDestinationTypes` -> the kit's category catalog. The flat agents-api list
// is split into the kit's two recognized categories ('self hosted'/'managed')
// via DESTINATION_CATEGORY. The per-field shape (raw `componentType`/
// `renderCondition`/`componentProperties` strings) matches what the odigos
// backend's `destinationCategories` returned, so the kit's downstream field
// parsing/rendering works unchanged.
export const mapVmDestinationTypesToCategories = (data: unknown): GetDestinationCategoriesResult => {
  const types = (data as GetDestinationTypesQuery | null | undefined)?.destinationTypes ?? [];

  const selfHosted = types.filter((t) => (DESTINATION_CATEGORY[t.type] ?? SELF_HOSTED) === SELF_HOSTED).map(toCatalogItem);
  const managed = types.filter((t) => DESTINATION_CATEGORY[t.type] === MANAGED).map(toCatalogItem);

  return {
    destinationCategories: {
      categories: [
        { name: SELF_HOSTED, description: 'Self-hosted destinations', items: selfHosted },
        { name: MANAGED, description: 'Managed destinations', items: managed },
      ],
    },
    // Field-level types diverge (raw wire strings vs the kit's parsed
    // YamlProperties), exactly as on the k8s path which casts the raw envelope.
  } as unknown as GetDestinationCategoriesResult;
};

// `GetDestinations` -> the kit's configured-destinations list.
export const mapVmDestinations = (data: unknown): GetDestinationsData => {
  const destinations = (data as GetDestinationsQuery | null | undefined)?.destinations ?? [];
  return { destinations: destinations.map((d) => mapVmDestinationEntity({ ...d, signals: d.signals as unknown as string[] })) };
};

// Inject `disabled` into DestinationFields when the connected schema has it but the
// floored dialect snapshot does not select it yet (pre-v0.24.0). No-op on v0.24.0+.
export const patchVmDestinationsQuery = (printedQuery: string, ctx: OperationContext): string => {
  if (!supportsDestinationDisabled(dialectVersion(ctx))) return printedQuery;
  return withFragmentSelections(printedQuery, 'Destination', 'disabled');
};

// ----- mutation variable mappers (-> agents-api `{ input }`) -----

export const vmCreateDestinationVariables = (vars: CreateDestinationVars, ctx?: OperationContext): Record<string, unknown> => {
  const d = vars.destination;
  const input: Record<string, unknown> = {
    name: d.name,
    type: d.type,
    config: fieldsToConfig(d.fields),
    signals: exportedToSignals(d.exportedSignals),
  };
  if (!ctx || supportsDestinationDisabled(dialectVersion(ctx))) {
    input.disabled = !!d.disabled;
  }
  return { input };
};

export const vmUpdateDestinationVariables = (vars: UpdateDestinationVars, ctx?: OperationContext): Record<string, unknown> => {
  const d = vars.destination;
  const input: Record<string, unknown> = {
    name: vars.id,
    type: d.type,
    config: fieldsToConfig(d.fields),
    signals: exportedToSignals(d.exportedSignals),
  };
  if (!ctx || supportsDestinationDisabled(dialectVersion(ctx))) {
    input.disabled = !!d.disabled;
  }
  return { input };
};

export const vmDeleteDestinationVariables = (vars: DeleteDestinationVars): Record<string, unknown> => ({ input: { name: vars.id } });

export const vmTestConnectionVariables = (vars: TestDestinationConnectionVars): Record<string, unknown> => {
  const d = vars.destination;
  return { input: { type: d.type, config: fieldsToConfig(d.fields) } };
};

// ----- mutation result mappers -----

export const vmCreateDestinationResult = (data: unknown): CreateDestinationResult | undefined => {
  const created = (data as { createDestination?: VmDestinationEntity } | null | undefined)?.createDestination;
  if (!created) return undefined;
  return { createNewDestination: mapVmDestinationEntity(created) };
};

export const vmUpdateDestinationResult = (data: unknown): UpdateDestinationData | undefined => {
  const updated = (data as { updateDestination?: { name: string } } | null | undefined)?.updateDestination;
  if (!updated) return undefined;
  return { updateDestination: { id: updated.name } };
};

export const vmDeleteDestinationResult = (data: unknown): DeleteDestinationResult | undefined => {
  const deleted = (data as { deleteDestination?: { deleted: boolean } } | null | undefined)?.deleteDestination;
  return { deleteDestination: !!deleted?.deleted };
};

// agents-api `testConnectionForDestination` -> kit `TestConnectionResponse`.
export const vmTestConnectionResult = (data: unknown): TestConnectionResponse | undefined => {
  const res = (data as { testConnectionForDestination?: { succeeded: boolean; statusCode: number; destinationType?: string | null; message?: string | null; reason?: string | null } } | null | undefined)?.testConnectionForDestination;
  if (!res) return undefined;
  return {
    succeeded: res.succeeded,
    statusCode: res.statusCode,
    destinationType: (res.destinationType ?? '') as DestinationTypes,
    message: res.message ?? null,
    reason: res.reason ?? null,
  };
};
