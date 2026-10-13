package notify

import (
	"encoding/json"
	"errors"
	"testing"
)

var testFields = []FieldSpec{
	{Key: "url", Type: FieldURL, Required: true},
	{Key: "token", Type: FieldSecret},
	{Key: "timeout", Type: FieldNumber, Default: "5"},
	{Key: "verbose", Type: FieldBool},
	{Key: "mode", Type: FieldSelect, Options: []string{"a", "b"}},
}

func TestValidateConfig(t *testing.T) {
	cfg, err := ValidateConfig(testFields, json.RawMessage(`{"url":"https://x.io/h","token":"t","verbose":"true","mode":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.String("url") != "https://x.io/h" || cfg.String("token") != "t" || !cfg.Bool("verbose") || cfg.String("mode") != "b" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Number("timeout", 0) != 5 {
		t.Errorf("default not applied: %v", cfg["timeout"])
	}

	for name, raw := range map[string]string{
		"missing required": `{}`,
		"bad url":          `{"url":"ftp://x"}`,
		"unknown key":      `{"url":"https://x.io","zzz":1}`,
		"bad select":       `{"url":"https://x.io","mode":"c"}`,
		"bad number":       `{"url":"https://x.io","timeout":"fast"}`,
		"not an object":    `[1]`,
	} {
		if _, err := ValidateConfig(testFields, json.RawMessage(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	var ve *ValidationError
	_, err = ValidateConfig(testFields, json.RawMessage(`{"mode":"c"}`))
	if !errors.As(err, &ve) || ve.Fields["url"] != "required" || ve.Fields["mode"] == "" {
		t.Errorf("validation error fields = %+v", err)
	}
}

func TestRedactAndMerge(t *testing.T) {
	stored := json.RawMessage(`{"url":"https://x.io","token":"secret"}`)
	red := Redact(testFields, stored)
	var m map[string]any
	_ = json.Unmarshal(red, &m)
	if m["token"] != RedactedSecret || m["url"] != "https://x.io" {
		t.Fatalf("redacted = %s", red)
	}

	merged := MergeSecrets(testFields, red, stored)
	_ = json.Unmarshal(merged, &m)
	if m["token"] != "secret" {
		t.Fatalf("round-tripping the redacted value should keep the secret: %s", merged)
	}
	merged = MergeSecrets(testFields, json.RawMessage(`{"url":"https://y.io","token":""}`), stored)
	_ = json.Unmarshal(merged, &m)
	if m["token"] != "secret" || m["url"] != "https://y.io" {
		t.Fatalf("empty secret should keep stored value: %s", merged)
	}
	merged = MergeSecrets(testFields, json.RawMessage(`{"url":"https://y.io","token":"new"}`), stored)
	_ = json.Unmarshal(merged, &m)
	if m["token"] != "new" {
		t.Fatalf("explicit new secret should win: %s", merged)
	}
}

func TestBackoff(t *testing.T) {
	if Backoff(0) != backoffSchedule[0] || Backoff(1) != backoffSchedule[0] {
		t.Error("first failure should use first step")
	}
	if Backoff(100) != backoffSchedule[len(backoffSchedule)-1] {
		t.Error("should cap at last step")
	}
}

func TestEventIDStable(t *testing.T) {
	a := Event{Key: "k", Action: "firing", Status: "open", Occurrences: 1}
	b := a
	if a.ID() != b.ID() || len(a.ID()) != 32 {
		t.Fatal("same event must hash equal")
	}
	b.Occurrences = 2
	if a.ID() == b.ID() {
		t.Fatal("changed occurrences must change the id")
	}
}
