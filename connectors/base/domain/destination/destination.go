package destination

import (
	"encoding/json"
	"fmt"

	"github.com/go-playground/validator/v10"
)

// Destination is a connector-local telemetry export target.
type Destination struct {
	Name    string         `json:"name" validate:"required"`
	Type    string         `json:"type" validate:"required"`
	Config  map[string]any `json:"config,omitempty"`
	Signals []string       `json:"signals" validate:"required,min=1,dive,oneof=TRACES METRICS LOGS PROFILES"`
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
