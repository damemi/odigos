import {
  InstrumentationRuleType as KitRuleType,
  type FieldTypes,
  type GetInstrumentationRuleTypesResult,
  type InstrumentationRuleTypeOption,
  type InstrumentationRuleYamlProperties,
  type ProgrammingLanguages,
} from '@odigos/ui-kit/types';

// Maps the agents-api `instrumentationRuleTypes` catalog into the ui-kit's
// GET_INSTRUMENTATION_RULE_TYPES result shape. Mirrors `mapVmActionTypesToOptions`.
//
// The agents-api rule surface models the instrumentation rule types a VM agent
// can run. Its catalog `type` strings use the agents-api enum spelling (e.g.
// `CodeAttributes`), which we translate to the kit's `InstrumentationRuleType`.
// Catalog entries the kit can't represent are dropped so they never appear in the
// rule picker.

// Intentionally typed as a plain record (not Record<string, KitRuleType>) so a VM
// catalog type with no kit equivalent maps to `undefined` and is filtered out.
const VM_TO_KIT_RULE_TYPE: { [vmType: string]: KitRuleType } = {
  CodeAttributes: KitRuleType.CodeAttributes,
  CustomInstrumentation: KitRuleType.CustomInstrumentation,
  HeadersCollection: KitRuleType.HeadersCollection,
  PayloadCollection: KitRuleType.PayloadCollection,
};

// Raw (untyped) shape of the agents-api `instrumentationRuleTypes` query. We avoid importing a
// generated type so this compiles against dialect versions that predate the
// catalog query; `vmQuery` already returns `undefined` (graceful no-op) for those.
interface RawRuleField {
  name: string;
  displayName: string;
  componentType: string;
  componentProperties: string;
  initialValue?: string | null;
  renderCondition?: string | null;
}
interface RawRuleTypeOption {
  type: string;
  displayName: string;
  description: string;
  supportedLanguages: string[];
  docsUrl: string;
  fields: RawRuleField[];
}

const toCatalogField = (f: RawRuleField): InstrumentationRuleYamlProperties => ({
  name: f.name,
  displayName: f.displayName,
  componentType: f.componentType as FieldTypes,
  componentProperties: f.componentProperties,
  initialValue: f.initialValue ?? '',
  renderCondition: f.renderCondition ? [f.renderCondition] : undefined,
});

export const mapVmRuleTypesToOptions = (data: unknown): GetInstrumentationRuleTypesResult => {
  const items = (data as { instrumentationRuleTypes?: RawRuleTypeOption[] } | null | undefined)?.instrumentationRuleTypes ?? [];

  const instrumentationRuleTypes: InstrumentationRuleTypeOption[] = items
    .map((item): InstrumentationRuleTypeOption | null => {
      const kitType = VM_TO_KIT_RULE_TYPE[item.type];
      if (!kitType) return null;

      return {
        type: kitType,
        displayName: item.displayName,
        description: item.description,
        // agents-api emits lowercase language spelling (e.g. "go", "java") which
        // matches the kit's ProgrammingLanguages enum string values 1:1.
        supportedLanguages: (item.supportedLanguages ?? []) as ProgrammingLanguages[],
        docsUrl: item.docsUrl,
        fields: (item.fields ?? []).map(toCatalogField),
      };
    })
    .filter((r): r is InstrumentationRuleTypeOption => r !== null);

  return { instrumentationRuleTypes };
};
