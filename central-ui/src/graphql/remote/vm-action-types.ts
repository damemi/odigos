import { ActionType as KitActionType, type SignalType, type ActionTypeOption, type ActionYamlProperties, type GetActionTypesResult, type FieldTypes } from '@odigos/ui-kit/types';

// Maps the agents-api `actionTypes` catalog into the ui-kit's GET_ACTION_TYPES
// result shape. Mirrors `mapVmDestinationTypesToCategories`.
//
// The agents-api action surface only models the action types a VM agent can run
// (currently URL templatization). Its catalog `type` strings use the agents-api
// enum spelling (e.g. `urlTemplatization`), which we translate to the kit's
// `ActionType`. Catalog entries the kit can't represent are dropped so they
// never appear in the action picker.

// Intentionally typed as a plain record (not Record<string, KitActionType>) so a
// VM catalog type with no kit equivalent maps to `undefined` and is filtered out.
const VM_TO_KIT_ACTION_TYPE: { [vmType: string]: KitActionType } = {
  urlTemplatization: KitActionType.URLTemplatization,
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

  return { actionTypes };
};
