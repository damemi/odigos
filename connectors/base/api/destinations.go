package api

// DefaultDestinationType is the Odigos catalog destination type used when a provider does not
// override supported destination types.
const DefaultDestinationType = "otlp"

// DestinationTypesProvider optionally overrides the base default destination catalog. When
// SupportedDestinationTypes returns a non-empty slice, those Odigos catalog type keys are accepted;
// otherwise the base falls back to DefaultDestinationType only.
type DestinationTypesProvider interface {
	SupportedDestinationTypes() []string
}

// SupportedDestinationTypes resolves the destination types a connector accepts from the provider,
// defaulting to OTLP-only when the provider does not implement DestinationTypesProvider or returns
// an empty list.
func SupportedDestinationTypes(p Provider) []string {
	if dtp, ok := p.(DestinationTypesProvider); ok {
		if types := dtp.SupportedDestinationTypes(); len(types) > 0 {
			return types
		}
	}
	return []string{DefaultDestinationType}
}
