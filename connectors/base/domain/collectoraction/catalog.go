package collectoraction

import "github.com/odigos-io/agents-api/go/server/graph/model"

const (
	templatizeLiteralsFieldJSON         = `{"tooltip":"When enabled, number and string literals in SQL queries are replaced with placeholders. Identifiers, bind/positional parameters, booleans, and NULL are left unchanged."}`
	removePostgresCastOperatorFieldJSON = `{"tooltip":"When enabled, PostgreSQL \"::type\" casts are stripped from query text (e.g. \"?::uuid\" becomes \"?\"), reducing cardinality for queries that only differ by cast type."}`
	extractAttributeFieldJSON           = `{"variant":"cards","wrapKey":"extractions","tooltip":"Extract a value from an existing attribute into a new attribute.","columns":[{"keyName":"method","componentType":"segment","defaultValue":"preset","options":[{"id":"preset","label":"Preset extract","rowTitle":"Data format","tooltip":"Extract a value from an existing attribute using a pre-set pattern (JSON, SQL, or Resource path)."},{"id":"regex","label":"Custom Regex","rowTitle":"Custom Regex","tooltip":"Extract a value using your own regular expression with a single capture group."}]},{"keyName":"dataFormat","componentType":"radioGroup","defaultValue":"json","renderCondition":["method","=","preset"],"options":[{"id":"json","label":"JSON"},{"id":"sql","label":"SQL"},{"id":"resource_path","label":"Resource path"}]},{"keyName":"lookupKey","label":"Lookup key","placeholder":"e.g. customer_id","renderCondition":["method","=","preset"],"copyByColumn":{"column":"dataFormat","map":{"json":{"label":"JSON key","placeholder":"e.g. customer_id","helpMessage":"Type the JSON key containing the value to extract."},"sql":{"label":"SQL key","placeholder":"e.g. invoice_id","helpMessage":"Type the SQL key containing the value to extract."},"resource_path":{"label":"Resource path key","placeholder":"e.g. tenants","helpMessage":"Type the resource name containing the value to extract."}}}},{"keyName":"regex","label":"Regex","placeholder":"e.g. id=([^&]+)","helpMessage":"A regex with a single capture group; the captured value becomes the new attribute.","fullRow":true,"withRegexTester":true,"renderCondition":["method","=","regex"]},{"keyName":"targetAttributeName","label":"New span attribute name","placeholder":"e.g. customerId","helpMessage":"The name of the new extracted attribute.","separator":"arrow"}]}`
	attributeNamesToDeleteFieldJSON     = `{"tooltip":"List of attribute names to delete from the selected signals."}`
	renamesFieldJSON                    = `{"titleKey":"Old key","titleValue":"New key","titleButton":"Add rename","tooltip":"Map of existing attribute names to their new names."}`
)

func Types() []*model.ActionTypeOption { return append(baseTypes(), additionalTypes()...) }

func additionalTypes() []*model.ActionTypeOption {
	traces := []model.TelemetrySignal{model.TelemetrySignalTraces}
	all := []model.TelemetrySignal{model.TelemetrySignalTraces, model.TelemetrySignalMetrics, model.TelemetrySignalLogs}
	return []*model.ActionTypeOption{
		{Type: string(TypeDbQueryTemplatization), DisplayName: "DB Query Templatization", Description: "Replace SQL literals and PostgreSQL casts to reduce query cardinality. Requires database query text collection on the source.", AllowedSignals: traces, DocsURL: "https://docs.odigos.io/pipeline/actions/attributes/dbquerytemplatization", Fields: []*model.ActionField{
			{Name: "templatizeLiterals", DisplayName: "Templatize literals", ComponentType: "toggle", ComponentProperties: templatizeLiteralsFieldJSON, InitialValue: stringPtr("true")},
			{Name: "removePostgresCastOperator", DisplayName: "Remove PostgreSQL cast operator", ComponentType: "toggle", ComponentProperties: removePostgresCastOperatorFieldJSON, InitialValue: stringPtr("false")},
		}},
		{Type: string(TypeInferDbAttributes), DisplayName: "Infer DB Attributes", Description: "Infer db.operation.name and db.collection.name from database query text. Requires database query text collection on the source.", AllowedSignals: traces, DocsURL: "https://docs.odigos.io/pipeline/actions/attributes/inferdbattributes", Fields: []*model.ActionField{}},
		{Type: string(TypeExtractAttribute), DisplayName: "Extract Attribute", Description: "Extract values from span attributes using JSON, SQL, resource paths, or a regular expression.", AllowedSignals: traces, DocsURL: "https://docs.odigos.io/pipeline/actions/attributes/extractattribute", Fields: []*model.ActionField{
			{Name: "extractAttribute", DisplayName: "Extractions", ComponentType: "multiTabledInput", ComponentProperties: extractAttributeFieldJSON, InitialValue: stringPtr(`[{"method":"preset","dataFormat":"json"}]`)},
		}},
		{Type: string(TypeDeleteAttribute), DisplayName: "Delete Attribute", Description: "Delete attributes from resources, instrumentation scopes, and records of the selected signals.", AllowedSignals: all, DocsURL: "https://docs.odigos.io/pipeline/actions/attributes/deleteattribute", Fields: []*model.ActionField{
			{Name: "attributeNamesToDelete", DisplayName: "Attributes to delete", ComponentType: "multiInput", ComponentProperties: attributeNamesToDeleteFieldJSON},
		}},
		{Type: string(TypeRenameAttribute), DisplayName: "Rename Attribute", Description: "Rename attributes on resources, instrumentation scopes, and records of the selected signals.", AllowedSignals: all, DocsURL: "https://docs.odigos.io/pipeline/actions/attributes/renameattribute", Fields: []*model.ActionField{
			{Name: "renames", DisplayName: "Attributes to rename", ComponentType: "keyValuePairs", ComponentProperties: renamesFieldJSON},
		}},
	}
}
