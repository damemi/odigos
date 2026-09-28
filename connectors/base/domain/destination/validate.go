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
	if normalized == CatalogTypeManagedCollector {
		return validateManagedCollector(dest)
	}
	if strings.TrimSpace(dest.Region) != "" {
		return fmt.Errorf("destination type %q is global and cannot set region", dest.Type)
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

func validateManagedCollector(dest Destination) error {
	region := strings.TrimSpace(dest.Region)
	if region == "" {
		return fmt.Errorf("destination type %q requires a region", dest.Type)
	}
	if region != dest.Region {
		return fmt.Errorf("destination type %q region must not contain leading or trailing whitespace", dest.Type)
	}
	if len(dest.Config) != 0 {
		return fmt.Errorf("destination type %q does not accept configuration fields", dest.Type)
	}
	return nil
}

// ValidateDestinations validates a complete destination set. Single-destination validation cannot
// enforce the regional marker's uniqueness. A marker may be created before its global terminal is
// configured; routing reports an error until an applicable terminal exists.
func (c Catalog) ValidateDestinations(dests []Destination) error {
	markers := make(map[string]string)
	for _, dest := range dests {
		if err := c.ValidateWithCatalog(dest); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(dest.Type), CatalogTypeManagedCollector) {
			region := strings.TrimSpace(dest.Region)
			if previous, exists := markers[region]; exists {
				return fmt.Errorf("managed Collector marker is already configured for region %q (destinations %q and %q)",
					region, previous, dest.Name)
			}
			markers[region] = dest.Name
		}
	}
	return nil
}
