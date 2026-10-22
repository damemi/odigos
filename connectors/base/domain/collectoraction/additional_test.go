package collectoraction

import (
	"encoding/json"
	"testing"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	"github.com/stretchr/testify/require"
)

func TestAdditionalActionsRoundTrip(t *testing.T) {
	enabled := true
	for _, input := range []model.CreateActionInput{
		{ActionType: model.ActionTypeDbQueryTemplatization, DbQueryTemplatization: &model.DbQueryTemplatizationConfigInput{TemplatizeLiterals: &enabled}},
		{ActionType: model.ActionTypeInferDbAttributes},
		{ActionType: model.ActionTypeExtractAttribute, ExtractAttribute: &model.ExtractAttributeConfigInput{Extractions: []*model.ExtractAttributeExtractionInput{{TargetAttributeName: "tenant", LookupKey: ptr("tenants"), DataFormat: ptr("resource_path")}}}},
		{ActionType: model.ActionTypeDeleteAttribute, DeleteAttribute: &model.DeleteAttributeConfigInput{AttributeNamesToDelete: []string{"user.email"}}},
		{ActionType: model.ActionTypeRenameAttribute, RenameAttribute: &model.RenameAttributeConfigInput{Renames: []*model.RenameAttributeEntryInput{{From: "old", To: "new"}}}},
	} {
		t.Run(string(input.ActionType), func(t *testing.T) {
			input.Name, input.Region = "test-action", ptr("us-east-1")
			input.Signals = []model.TelemetrySignal{model.TelemetrySignalTraces}
			a, err := FromCreateInput(input)
			require.NoError(t, err)
			mapped, err := ToModel(a)
			require.NoError(t, err)
			require.Equal(t, input.ActionType, mapped.GetActionType())
			require.Equal(t, input.Region, mapped.GetRegion())
			disabled := true
			next, err := ApplyUpdateInput(a, model.UpdateActionInput{Name: a.Name, Region: &a.Region, Disabled: &disabled})
			require.NoError(t, err)
			require.JSONEq(t, string(a.Config), string(next.Config))
			require.True(t, next.Disabled)
		})
	}
}

func TestDbQueryPatchPreservesOmittedOptions(t *testing.T) {
	current := Action{Region: "us-east-1", Name: "db", Type: TypeDbQueryTemplatization, Signals: []string{"TRACES"}, Config: json.RawMessage(`{"templatizeLiterals":true,"removePostgresCastOperator":true}`)}
	disabled := false
	next, err := ApplyUpdateInput(current, model.UpdateActionInput{Region: &current.Region, DbQueryTemplatization: &model.DbQueryTemplatizationConfigInput{TemplatizeLiterals: &disabled}})
	require.NoError(t, err)
	require.JSONEq(t, `{"templatizeLiterals":false,"removePostgresCastOperator":true}`, string(next.Config))
}

func TestAdditionalActionValidation(t *testing.T) {
	for _, tc := range []struct {
		typ          Type
		config, want string
	}{
		{TypeDbQueryTemplatization, `{}`, "option enabled"},
		{TypeDeleteAttribute, `{"attributeNamesToDelete":[""]}`, "blank"},
		{TypeRenameAttribute, `{"renames":[{"from":"old","to":"old"}]}`, "differ"},
		{TypeRenameAttribute, `{"renames":[{"from":"old","to":"one"},{"from":"old","to":"two"}]}`, "duplicate"},
		{TypeExtractAttribute, `{"extractions":[{"targetAttributeName":"x","regex":"("}]}`, "regex"},
		{TypeExtractAttribute, `{"extractions":[{"targetAttributeName":"x","regex":"no-capture"}]}`, "capture group"},
		{TypeExtractAttribute, `{"extractions":[{"targetAttributeName":"x","lookupKey":"a","dataFormat":"xml"}]}`, "data format"},
		{TypeExtractAttribute, `{"extractions":[null]}`, "null"},
		{TypeInferDbAttributes, `null`, "configuration"},
	} {
		t.Run(string(tc.typ)+tc.config, func(t *testing.T) {
			a := Action{Region: "us-east-1", Name: "test", Type: tc.typ, Signals: []string{"TRACES"}, Config: json.RawMessage(tc.config)}
			require.ErrorContains(t, Validate(a), tc.want)
		})
	}
}

func TestCatalogMatchesManagedCollectorSignals(t *testing.T) {
	catalog := Types()
	require.Len(t, catalog, 8)
	for _, typ := range catalog {
		require.NotEqual(t, "piiMasking", typ.Type)
		require.NotContains(t, typ.AllowedSignals, model.TelemetrySignalProfiles)
		for _, field := range typ.Fields {
			if field.ComponentProperties != "" {
				require.True(t, json.Valid([]byte(field.ComponentProperties)), field.Name)
			}
		}
	}
}
