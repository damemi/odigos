package destination

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	odigosdest "github.com/odigos-io/odigos/destinations"
)

// CatalogTypeOTLP is the default Odigos destination type when a provider does not override supported types.
const CatalogTypeOTLP = "otlp"

var defaultSupportedTypes = []string{CatalogTypeOTLP}

// Catalog is the set of Odigos destination type keys a connector accepts.
type Catalog struct {
	supportedTypes []string
}

// DefaultCatalog returns the base default catalog (OTLP only).
func DefaultCatalog() Catalog {
	return NewCatalog(nil)
}

// NewCatalog builds a catalog from the given Odigos destination type keys (case-insensitive). An empty
// or all-blank slice falls back to OTLP only.
func NewCatalog(types []string) Catalog {
	if len(types) == 0 {
		return Catalog{supportedTypes: append([]string{}, defaultSupportedTypes...)}
	}
	normalized := make([]string, 0, len(types))
	seen := make(map[string]bool)
	for _, t := range types {
		key := strings.ToLower(strings.TrimSpace(t))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return Catalog{supportedTypes: append([]string{}, defaultSupportedTypes...)}
	}
	return Catalog{supportedTypes: normalized}
}

// SupportedTypes returns a copy of the accepted Odigos catalog destination type keys.
func (c Catalog) SupportedTypes() []string {
	out := make([]string, len(c.supportedTypes))
	copy(out, c.supportedTypes)
	return out
}

// IsSupported reports whether destType is accepted by this catalog (case-insensitive).
func (c Catalog) IsSupported(destType string) bool {
	key := strings.ToLower(strings.TrimSpace(destType))
	for _, t := range c.supportedTypes {
		if t == key {
			return true
		}
	}
	return false
}

// DestinationTypeFromCatalog converts an Odigos destination catalog entry to the GraphQL model.
func DestinationTypeFromCatalog(destConfig odigosdest.Destination) *model.DestinationType {
	fields := make([]*model.DestinationField, 0, len(destConfig.Spec.Fields))
	for _, field := range destConfig.Spec.Fields {
		componentPropsJSON, err := json.Marshal(field.ComponentProps)
		if err != nil {
			componentPropsJSON = []byte("{}")
		}
		customLabels := make([]*model.CustomReadDataLabel, 0, len(field.CustomReadDataLabels))
		for _, label := range field.CustomReadDataLabels {
			customLabels = append(customLabels, &model.CustomReadDataLabel{
				Condition: label.Condition,
				Title:     label.Title,
				Value:     label.Value,
			})
		}
		iv := field.InitialValue
		var rend *string
		if len(field.RenderCondition) > 0 {
			s := strings.Join(field.RenderCondition, ",")
			rend = &s
		}
		fields = append(fields, &model.DestinationField{
			Name:                 field.Name,
			DisplayName:          field.DisplayName,
			ComponentType:        field.ComponentType,
			ComponentProperties:  string(componentPropsJSON),
			Secret:               field.Secret,
			InitialValue:         &iv,
			RenderCondition:      rend,
			HideFromReadData:     len(field.HideFromReadData) > 0,
			CustomReadDataLabels: customLabels,
		})
	}
	return &model.DestinationType{
		Type:        string(destConfig.Metadata.Type),
		DisplayName: destConfig.Metadata.DisplayName,
		ImageURL:    "",
		SupportedSignals: &model.DestinationSupportedSignals{
			Traces:   destConfig.Spec.Signals.Traces.Supported,
			Metrics:  destConfig.Spec.Signals.Metrics.Supported,
			Logs:     destConfig.Spec.Signals.Logs.Supported,
			Profiles: destConfig.Spec.Signals.Profiles.Supported,
		},
		Fields: fields,
	}
}

// SupportedCatalogDestinationTypes returns the Odigos catalog entries for every type in this catalog.
func (c Catalog) SupportedCatalogDestinationTypes() ([]*model.DestinationType, error) {
	if err := odigosdest.Load(); err != nil {
		return nil, err
	}
	var out []*model.DestinationType
	for _, t := range c.supportedTypes {
		cfg, ok := odigosdest.GetDestinationByType(t)
		if !ok {
			continue
		}
		out = append(out, DestinationTypeFromCatalog(cfg))
	}
	return out, nil
}

func (c Catalog) supportedTypesLabel() string {
	return strings.Join(c.supportedTypes, ", ")
}

func (c Catalog) unsupportedTypeError(destType string) error {
	return fmt.Errorf("destination type %q is not supported by this connector (supported: %s)", destType, c.supportedTypesLabel())
}
