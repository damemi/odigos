package collectoraction

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/api/actions"
)

func configBranches(url *model.URLTemplatizationConfigInput, processor *model.ProcessorAdditionConfigInput, span *model.SpanMetricsConfigInput, db *model.DbQueryTemplatizationConfigInput, extract *model.ExtractAttributeConfigInput, del *model.DeleteAttributeConfigInput, rename *model.RenameAttributeConfigInput, pii *model.PiiMaskingConfigInput) map[Type]any {
	out := map[Type]any{}
	if url != nil {
		out[TypeURLTemplatization] = url
	}
	if processor != nil {
		out[TypeProcessorAddition] = processor
	}
	if span != nil {
		out[TypeSpanMetrics] = span
	}
	if db != nil {
		out[TypeDbQueryTemplatization] = db
	}
	if extract != nil {
		out[TypeExtractAttribute] = extract
	}
	if del != nil {
		out[TypeDeleteAttribute] = del
	}
	if rename != nil {
		out[TypeRenameAttribute] = rename
	}
	if pii != nil {
		out[Type("piiMasking")] = pii
	}
	return out
}

func inputConfig(typ Type, branches map[Type]any, partial bool) (any, error) {
	if typ == TypeInferDbAttributes && len(branches) == 0 {
		return map[string]any{}, nil
	}
	if partial && len(branches) == 0 {
		return nil, nil
	}
	if len(branches) > 1 {
		return nil, fmt.Errorf("exactly one config branch must match actionType %q", typ)
	}
	if len(branches) == 0 || branches[typ] == nil {
		return nil, fmt.Errorf("config must match actionType %q", typ)
	}
	return branches[typ], nil
}

func validateAdditionalAction(a Action) error {
	if a.Type == TypeDbQueryTemplatization || a.Type == TypeInferDbAttributes || a.Type == TypeExtractAttribute {
		if len(a.Signals) != 1 || a.Signals[0] != "TRACES" {
			return fmt.Errorf("action %q supports TRACES only", a.Name)
		}
	}
	switch a.Type {
	case TypeDbQueryTemplatization:
		var cfg model.DbQueryTemplatizationConfigInput
		if err := json.Unmarshal(a.Config, &cfg); err != nil {
			return err
		}
		if (cfg.TemplatizeLiterals == nil || !*cfg.TemplatizeLiterals) && (cfg.RemovePostgresCastOperator == nil || !*cfg.RemovePostgresCastOperator) {
			return fmt.Errorf("DB query templatization needs at least one option enabled")
		}
	case TypeInferDbAttributes:
		var cfg map[string]any
		if err := json.Unmarshal(a.Config, &cfg); err != nil {
			return err
		}
		if cfg == nil || len(cfg) != 0 {
			return fmt.Errorf("infer DB attributes has no configuration fields")
		}
	case TypeDeleteAttribute:
		var cfg model.DeleteAttributeConfigInput
		if err := json.Unmarshal(a.Config, &cfg); err != nil {
			return err
		}
		if len(cfg.AttributeNamesToDelete) == 0 {
			return fmt.Errorf("attributeNamesToDelete must not be empty")
		}
		for _, name := range cfg.AttributeNamesToDelete {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("attribute names must not be blank")
			}
		}
	case TypeRenameAttribute:
		_, err := renameConfig(a.Config)
		if err != nil {
			return err
		}
	case TypeExtractAttribute:
		cfg, err := extractionConfig(a.Config)
		if err != nil {
			return err
		}
		for _, extraction := range cfg.Extractions {
			if strings.TrimSpace(extraction.TargetAttributeName) == "" {
				return fmt.Errorf("targetAttributeName must not be blank")
			}
			if extraction.DataFormat != "" && extraction.DataFormat != "json" && extraction.DataFormat != "sql" && extraction.DataFormat != "resource_path" {
				return fmt.Errorf("unsupported extraction data format %q", extraction.DataFormat)
			}
			if extraction.Regex != "" {
				re, err := regexp.Compile(extraction.Regex)
				if err != nil {
					return fmt.Errorf("invalid extraction regex: %w", err)
				}
				if re.NumSubexp() != 1 {
					return fmt.Errorf("extraction regex must contain exactly one capture group")
				}
			}
		}
		_, err = actions.BuildExtractAttributeProcessorConfig(&cfg)
		return err
	}
	return nil
}

func extractionConfig(raw json.RawMessage) (actions.ExtractAttributeConfig, error) {
	var input model.ExtractAttributeConfigInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return actions.ExtractAttributeConfig{}, err
	}
	out := actions.ExtractAttributeConfig{}
	for _, entry := range input.Extractions {
		if entry == nil {
			return out, fmt.Errorf("extractions must not contain null entries")
		}
		extraction := actions.Extraction{TargetAttributeName: entry.TargetAttributeName}
		if entry.LookupKey != nil {
			extraction.LookupKey = *entry.LookupKey
		}
		if entry.DataFormat != nil {
			extraction.DataFormat = actions.DataFormat(*entry.DataFormat)
		}
		if entry.Regex != nil {
			extraction.Regex = *entry.Regex
		}
		out.Extractions = append(out.Extractions, extraction)
	}
	return out, nil
}

func renameConfig(raw json.RawMessage) (map[string]string, error) {
	var cfg model.RenameAttributeConfigInput
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.Renames) == 0 {
		return nil, fmt.Errorf("renames must not be empty")
	}
	out := map[string]string{}
	for _, entry := range cfg.Renames {
		if entry == nil || strings.TrimSpace(entry.From) == "" || strings.TrimSpace(entry.To) == "" {
			return nil, fmt.Errorf("each rename needs non-blank from and to attribute names")
		}
		if _, exists := out[entry.From]; exists {
			return nil, fmt.Errorf("duplicate rename source %q", entry.From)
		}
		if entry.From == entry.To {
			return nil, fmt.Errorf("rename source and target must differ")
		}
		out[entry.From] = entry.To
	}
	return out, nil
}

// AttributeProcessorConfig shares vm-agent's builders, including resource, scope and record attributes.
func AttributeProcessorConfig(a Action) (string, any, error) {
	signals := make([]common.ObservabilitySignal, len(a.Signals))
	for i, signal := range a.Signals {
		signals[i] = common.ObservabilitySignal(signal)
	}
	switch a.Type {
	case TypeExtractAttribute:
		cfg, err := extractionConfig(a.Config)
		if err != nil {
			return "", nil, err
		}
		out, err := actions.BuildExtractAttributeProcessorConfig(&cfg)
		return "odigosextractattribute", out, err
	case TypeDeleteAttribute:
		var cfg model.DeleteAttributeConfigInput
		if err := json.Unmarshal(a.Config, &cfg); err != nil {
			return "", nil, err
		}
		out, err := actions.BuildDeleteAttributeProcessorConfig(cfg.AttributeNamesToDelete, signals)
		return "transform", out, err
	case TypeRenameAttribute:
		cfg, err := renameConfig(a.Config)
		if err != nil {
			return "", nil, err
		}
		out, err := actions.BuildRenameAttributeProcessorConfig(cfg, signals)
		return "transform", out, err
	}
	return "", nil, fmt.Errorf("unsupported attribute action %q", a.Type)
}
