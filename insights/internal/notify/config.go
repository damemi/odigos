package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// RedactedSecret is what secret fields read back as from the API and debug
// UI. Writing it (or an empty string) back for a secret keeps the stored value
// (see MergeSecrets), so an edit form never needs to re-enter credentials.
const RedactedSecret = "••••••••"

// ValidationError lists per-field problems so a form can show them inline.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for k, v := range e.Fields {
		parts = append(parts, k+": "+v)
	}
	return "invalid config: " + strings.Join(parts, "; ")
}

// ValidateConfig parses raw JSON config against the descriptor's fields and
// returns the typed Config. Unknown keys are rejected so typos surface
// instead of silently falling back to defaults.
func ValidateConfig(fields []FieldSpec, raw json.RawMessage) (Config, error) {
	in := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("config is not a JSON object: %w", err)
		}
	}
	known := make(map[string]FieldSpec, len(fields))
	for _, f := range fields {
		known[f.Key] = f
	}
	problems := map[string]string{}
	for k := range in {
		if _, ok := known[k]; !ok {
			problems[k] = "unknown field"
		}
	}
	out := make(Config, len(fields))
	for _, f := range fields {
		v, present := in[f.Key]
		if !present || v == nil || v == "" {
			if f.Default != "" {
				v = f.Default
			} else if f.Required {
				problems[f.Key] = "required"
				continue
			} else {
				continue
			}
		}
		typed, err := coerce(f, v)
		if err != nil {
			problems[f.Key] = err.Error()
			continue
		}
		out[f.Key] = typed
	}
	if len(problems) > 0 {
		return nil, &ValidationError{Fields: problems}
	}
	return out, nil
}

func coerce(f FieldSpec, v any) (any, error) {
	switch f.Type {
	case FieldString, FieldSecret:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		return s, nil
	case FieldURL:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("must be an http(s) URL")
		}
		return s, nil
	case FieldNumber:
		switch n := v.(type) {
		case float64:
			return n, nil
		case string:
			f, err := strconv.ParseFloat(n, 64)
			if err != nil {
				return nil, errors.New("must be a number")
			}
			return f, nil
		}
		return nil, errors.New("must be a number")
	case FieldBool:
		switch b := v.(type) {
		case bool:
			return b, nil
		case string:
			switch strings.ToLower(b) {
			case "true", "on", "1", "yes":
				return true, nil
			case "false", "off", "0", "no":
				return false, nil
			}
		}
		return nil, errors.New("must be a boolean")
	case FieldSelect:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		for _, o := range f.Options {
			if o == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("must be one of %s", strings.Join(f.Options, ", "))
	}
	return nil, fmt.Errorf("unsupported field type %q", f.Type)
}

// Redact returns raw with every secret field replaced by RedactedSecret, for
// read-back through the API and debug UI. Non-object or empty input is
// returned as "{}".
func Redact(fields []FieldSpec, raw json.RawMessage) json.RawMessage {
	in := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return json.RawMessage(`{}`)
		}
	}
	for _, f := range fields {
		if f.Type != FieldSecret {
			continue
		}
		if s, ok := in[f.Key].(string); ok && s != "" {
			in[f.Key] = RedactedSecret
		}
	}
	out, err := json.Marshal(in)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

// MergeSecrets returns incoming with every secret field that is empty or
// RedactedSecret replaced by the value from existing, so an update that
// round-trips a redacted read does not wipe credentials.
func MergeSecrets(fields []FieldSpec, incoming, existing json.RawMessage) json.RawMessage {
	in := map[string]any{}
	if len(incoming) > 0 {
		if err := json.Unmarshal(incoming, &in); err != nil {
			return incoming
		}
	}
	old := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &old)
	}
	for _, f := range fields {
		if f.Type != FieldSecret {
			continue
		}
		s, _ := in[f.Key].(string)
		if s == "" || s == RedactedSecret {
			if prev, ok := old[f.Key]; ok {
				in[f.Key] = prev
			} else {
				delete(in, f.Key)
			}
		}
	}
	out, err := json.Marshal(in)
	if err != nil {
		return incoming
	}
	return out
}
