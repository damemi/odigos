package destination

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/destinations"
)

// ValidateWithCatalog ensures the destination type is allowed by the catalog and that config/signals
// match the Odigos destination catalog entry.
func (c Catalog) ValidateWithCatalog(dest Destination) error {
	normalized := strings.ToLower(strings.TrimSpace(dest.Type))
	if !c.IsSupported(normalized) {
		return c.unsupportedTypeError(dest.Type)
	}
	if err := dest.Validate(); err != nil {
		return err
	}
	if err := destinations.Load(); err != nil {
		return fmt.Errorf("load destination catalog: %w", err)
	}
	odigosDest, ok := destinations.GetDestinationByType(normalized)
	if !ok {
		return fmt.Errorf("destination type %q is not available in the Odigos catalog", normalized)
	}

	var missingFields []string
	for _, field := range odigosDest.Spec.Fields {
		if field.ComponentProps["required"] == true {
			fieldName := field.Name
			if _, ok := dest.Config[fieldName]; !ok {
				missingFields = append(missingFields, fieldName)
			} else if value, ok := dest.Config[fieldName].(string); ok && strings.TrimSpace(value) == "" {
				missingFields = append(missingFields, fieldName)
			}
		}
	}
	if len(missingFields) > 0 {
		return fmt.Errorf("missing required fields for destination type %q: %s", dest.Type, strings.Join(missingFields, ", "))
	}

	var unsupportedSignals []string
	for _, signal := range dest.Signals {
		switch signal {
		case "TRACES":
			if !odigosDest.Spec.Signals.Traces.Supported {
				unsupportedSignals = append(unsupportedSignals, signal)
			}
		case "METRICS":
			if !odigosDest.Spec.Signals.Metrics.Supported {
				unsupportedSignals = append(unsupportedSignals, signal)
			}
		case "LOGS":
			if !odigosDest.Spec.Signals.Logs.Supported {
				unsupportedSignals = append(unsupportedSignals, signal)
			}
		case "PROFILES":
			if !odigosDest.Spec.Signals.Profiles.Supported {
				unsupportedSignals = append(unsupportedSignals, signal)
			}
		default:
			return fmt.Errorf("invalid signal %q", signal)
		}
	}
	if len(unsupportedSignals) > 0 {
		return fmt.Errorf("destination type %q does not support signals: %s", dest.Type, strings.Join(unsupportedSignals, ", "))
	}
	return nil
}
