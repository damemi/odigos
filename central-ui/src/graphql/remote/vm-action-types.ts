import { ActionType as KitActionType, FieldTypes, SignalType, type ActionTypeOption, type ActionYamlProperties, type GetActionTypesResult } from '@odigos/ui-kit/types';

// Maps the agents-api `actionTypes` catalog into the ui-kit's GET_ACTION_TYPES
// result shape. Mirrors `mapVmDestinationTypesToCategories`.
//
// Catalog type strings use agents-api enum spellings, translated to UI kit names.

const VM_TO_KIT_ACTION_TYPE: { [vmType: string]: KitActionType } = {
  urlTemplatization: KitActionType.URLTemplatization,
  processorAddition: KitActionType.ProcessorAddition,
  spanMetrics: KitActionType.SpanMetrics,
  dbQueryTemplatization: KitActionType.DbQueryTemplatization,
  inferDbAttributes: KitActionType.InferDbAttributes,
  extractAttribute: KitActionType.ExtractAttribute,
  deleteAttribute: KitActionType.DeleteAttributes,
  renameAttribute: KitActionType.RenameAttributes,
};

// Raw (untyped) shape of the agents-api `actionTypes` query. We avoid importing a
// generated type so this compiles against dialect versions that predate the
// catalog query; `vmQuery` already returns `undefined` (graceful no-op) for those.
interface RawActionField {
  name: string;
  displayName: string;
  componentType: string;
  componentProperties: string;
  initialValue?: string | null;
  renderCondition?: string | null;
}
interface RawActionTypeOption {
  type: string;
  displayName: string;
  description: string;
  allowedSignals: string[];
  docsUrl: string;
  fields: RawActionField[];
}

const toCatalogField = (f: RawActionField): ActionYamlProperties => ({
  name: f.name,
  displayName: f.displayName,
  componentType: f.componentType as FieldTypes,
  componentProperties: f.componentProperties,
  initialValue: f.initialValue ?? '',
  renderCondition: f.renderCondition ? [f.renderCondition] : undefined,
});

// The connector and VM agent both publish this catalog. Keep a local fallback
// for the two non-URL types while an already-connected proxy is catching up to
// a new catalog response: unlike falling back to the general k8s action list,
// these fields and signals exactly match the agents-api contracts.
const COLLECTOR_ACTION_FALLBACKS: ActionTypeOption[] = [
  {
    type: KitActionType.ProcessorAddition,
    displayName: 'Processor Addition',
    description: 'Add an approved OpenTelemetry processor to this collector.',
    allowedSignals: [SignalType.Traces, SignalType.Metrics, SignalType.Logs],
    docsUrl: '',
    fields: [
      { name: 'processorType', displayName: 'Processor type', componentType: FieldTypes.Dropdown, componentProperties: '{"options":[{"id":"attributes","label":"Attributes"},{"id":"filter","label":"Filter"},{"id":"transform","label":"Transform"},{"id":"resource","label":"Resource"},{"id":"groupbyattrs","label":"Group by attributes"},{"id":"span","label":"Span"},{"id":"redaction","label":"Redaction"},{"id":"probabilistic_sampler","label":"Probabilistic sampler"},{"id":"metricstransform","label":"Metrics transform"},{"id":"cumulativetodelta","label":"Cumulative to delta"},{"id":"deltatorate","label":"Delta to rate"},{"id":"metricsgeneration","label":"Metrics generation"},{"id":"odigosextractattribute","label":"Extract attribute"}]}', initialValue: 'attributes' },
      { name: 'processorName', displayName: 'Processor name', componentType: FieldTypes.Input, componentProperties: '{"placeholder":"Instance name (required for resource)"}' },
      { name: 'processorConfig', displayName: 'Processor configuration (JSON)', componentType: FieldTypes.TextArea, componentProperties: '{"placeholder":"{}","required":true}', initialValue: '{}' },
    ],
  },
  {
    type: KitActionType.SpanMetrics,
    displayName: 'Span Metrics',
    description: 'Generate metrics from traces received by this collector.',
    allowedSignals: [SignalType.Traces],
    docsUrl: '',
    fields: [
      { name: 'spanMetricsDisabled', displayName: 'Disable span metrics', componentType: FieldTypes.Toggle, initialValue: 'false' },
      { name: 'interval', displayName: 'Interval', componentType: FieldTypes.Input, componentProperties: '{"placeholder":"e.g. 15s"}' },
      { name: 'metricsExpiration', displayName: 'Metrics expiration', componentType: FieldTypes.Input, componentProperties: '{"placeholder":"e.g. 5m"}' },
      { name: 'additionalDimensions', displayName: 'Additional dimensions', componentType: FieldTypes.MultiInput, componentProperties: '{"placeholder":"e.g. service.namespace"}' },
      { name: 'histogramDisabled', displayName: 'Disable latency histogram', componentType: FieldTypes.Toggle, initialValue: 'false' },
      { name: 'explicitHistogramBuckets', displayName: 'Explicit histogram buckets', componentType: FieldTypes.MultiInput, componentProperties: '{"placeholder":"e.g. 100ms"}' },
      { name: 'resourceMetricsKeyAttributes', displayName: 'Resource metric key attributes', componentType: FieldTypes.MultiInput, componentProperties: '{"placeholder":"e.g. service.name"}' },
      { name: 'includedProcessInDimensions', displayName: 'Include process in dimensions', componentType: FieldTypes.Toggle, initialValue: 'false' },
      { name: 'excludedResourceAttributes', displayName: 'Excluded resource attributes', componentType: FieldTypes.MultiInput, componentProperties: '{"placeholder":"e.g. process.command_line"}' },
    ],
  },
];

export const mapVmActionTypesToOptions = (data: unknown): GetActionTypesResult => {
  const items = (data as { actionTypes?: RawActionTypeOption[] } | null | undefined)?.actionTypes ?? [];

  const actionTypes: ActionTypeOption[] = items
    .map((item): ActionTypeOption | null => {
      const kitType = VM_TO_KIT_ACTION_TYPE[item.type];
      if (!kitType) return null;

      return {
        type: kitType,
        displayName: item.displayName,
        description: item.description,
        // agents-api TelemetrySignal and kit SignalType share identical string
        // values (TRACES / METRICS / LOGS / PROFILES).
        allowedSignals: (item.allowedSignals ?? []) as SignalType[],
        docsUrl: item.docsUrl,
        fields: (item.fields ?? []).map(toCatalogField),
      };
    })
    .filter((a): a is ActionTypeOption => a !== null);

  return { actionTypes: [...actionTypes, ...COLLECTOR_ACTION_FALLBACKS.filter((fallback) => !actionTypes.some((item) => item.type === fallback.type))] };
};
