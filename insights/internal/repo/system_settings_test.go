package repo

import "testing"

func TestSystemSettingsValidate(t *testing.T) {
	s := DefaultSystemSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if s.ObservationRetentionDays != DefaultObservationRetentionDays {
		t.Fatalf("retention default = %d, want %d", s.ObservationRetentionDays, DefaultObservationRetentionDays)
	}

	bad := s
	bad.ObservationRetentionDays = 121
	if err := bad.Validate(); err == nil {
		t.Fatal("expected retention > max to fail")
	}
}

func TestSystemSettingsWithDefaults(t *testing.T) {
	var zero SystemSettings
	got := zero.WithDefaults()
	if got.ExamplesPerTransaction != DefaultExamplesPerTransaction {
		t.Fatalf("examples = %d", got.ExamplesPerTransaction)
	}
	if got.MaxResidentTransactions != DefaultMaxResidentTransactions {
		t.Fatalf("max resident = %d", got.MaxResidentTransactions)
	}
	if got.ObservationRetentionDays != DefaultObservationRetentionDays {
		t.Fatalf("retention = %d", got.ObservationRetentionDays)
	}
	// bool zero stays false; default true only via DefaultSystemSettings
	if got.AutoTransactionGuardrail {
		t.Fatal("zero WithDefaults should not force auto inventory true")
	}
	if !DefaultSystemSettings().AutoTransactionGuardrail {
		t.Fatal("DefaultSystemSettings should enable auto inventory")
	}
	if len(got.TransactionIdentityDimensions) == 0 {
		t.Fatal("nil dimensions should default to status-code dims")
	}
	empty := SystemSettings{TransactionIdentityDimensions: []TransactionIdentityDimension{}}
	if len(empty.WithDefaults().TransactionIdentityDimensions) != 0 {
		t.Fatal("explicit empty dimensions must stay empty")
	}
}

func TestIdentityDimensionsFingerprint(t *testing.T) {
	base := []TransactionIdentityDimension{
		{Key: "a", Enabled: true},
		{Key: "b", Enabled: true},
	}
	want := IdentityDimensionsFingerprint(base)

	// Toggling a dimension the identity does not use must not change the stamp:
	// disabled keys never reach an operation string.
	withDisabled := append([]TransactionIdentityDimension{{Key: "z", Enabled: false}}, base...)
	if got := IdentityDimensionsFingerprint(withDisabled); got != want {
		t.Fatalf("disabled dimension changed fingerprint: %q != %q", got, want)
	}

	// Enabling one, dropping one, or reordering all change how identities are
	// built, so each must produce a different stamp.
	for name, dims := range map[string][]TransactionIdentityDimension{
		"added":     {{Key: "a", Enabled: true}, {Key: "b", Enabled: true}, {Key: "c", Enabled: true}},
		"removed":   {{Key: "a", Enabled: true}},
		"reordered": {{Key: "b", Enabled: true}, {Key: "a", Enabled: true}},
		"disabled":  {{Key: "a", Enabled: true}, {Key: "b", Enabled: false}},
	} {
		if got := IdentityDimensionsFingerprint(dims); got == want {
			t.Fatalf("%s dimensions must not share the base fingerprint", name)
		}
	}
}

func TestTransactionIdentityDimensionsValidate(t *testing.T) {
	s := DefaultSystemSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}

	s.TransactionIdentityDimensions = []TransactionIdentityDimension{
		{Key: "", Enabled: true},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("expected empty key to fail")
	}

	s.TransactionIdentityDimensions = []TransactionIdentityDimension{
		{Key: "a", Enabled: true},
		{Key: "a", Enabled: false},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("expected duplicate key to fail")
	}

	tooMany := make([]TransactionIdentityDimension, MaxTransactionIdentityDimensions+1)
	for i := range tooMany {
		tooMany[i] = TransactionIdentityDimension{Key: string(rune('a' + i)), Enabled: true}
	}
	s.TransactionIdentityDimensions = tooMany
	if err := s.Validate(); err == nil {
		t.Fatal("expected too many enabled dims to fail")
	}

	// Disabled dims do not count toward the enabled cap.
	many := make([]TransactionIdentityDimension, MaxTransactionIdentityDimensions+2)
	for i := range many {
		many[i] = TransactionIdentityDimension{Key: string(rune('a' + i)), Enabled: i < MaxTransactionIdentityDimensions}
	}
	s.TransactionIdentityDimensions = many
	if err := s.Validate(); err != nil {
		t.Fatalf("disabled extras should be ok: %v", err)
	}
}
