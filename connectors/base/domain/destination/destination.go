package destination

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Destination is a connector-local telemetry export target.
type Destination struct {
	Name string `json:"name" validate:"required"`
	Type string `json:"type" validate:"required"`
	// Region scopes the managed-collector routing marker. Ordinary OTLP destinations are
	// connector-wide terminals and must leave this empty.
	Region   string         `json:"region,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
	Signals  []string       `json:"signals" validate:"required,min=1,dive,oneof=TRACES METRICS LOGS PROFILES"`
	Disabled bool           `json:"disabled,omitempty"`
}

func isManagedCollectorType(typ string) bool {
	return strings.EqualFold(strings.TrimSpace(typ), CatalogTypeManagedCollector)
}

// IsManagedCollectorType identifies the connector-owned routing marker without exposing the
// case/whitespace normalization details to callers.
func IsManagedCollectorType(typ string) bool {
	return isManagedCollectorType(typ)
}

// Validate runs field-level validation rules.
func (d *Destination) Validate() error {
	return validator.New().Struct(d)
}

func (d Destination) String() string {
	b, err := json.Marshal(d)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	return string(b)
}
