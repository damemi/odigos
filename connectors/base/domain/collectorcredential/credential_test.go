package collectorcredential

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const tokenValue = "opamp-tok-4f9c2b1e-do-not-log-me"

// TestSecretNeverRendersItsValue is the acceptance criterion "no plaintext credential appears in
// definitions, logs, or artifacts", enforced at the type rather than by convention. Every implicit path
// out of a Secret is covered here, because the one that is not covered is the one a debugging session
// finds.
func TestSecretNeverRendersItsValue(t *testing.T) {
	s := NewSecret(tokenValue)
	renders := map[string]string{
		"%v":          fmt.Sprintf("%v", s),
		"%s":          fmt.Sprintf("%s", s),
		"%q":          fmt.Sprintf("%q", s),
		"%#v":         fmt.Sprintf("%#v", s),
		"%+v":         fmt.Sprintf("%+v", s),
		"String()":    s.String(),
		"in a struct": fmt.Sprintf("%v", struct{ Cred Secret }{s}),
		"in a slice":  fmt.Sprintf("%v", []Secret{s}),
		"in a map":    fmt.Sprintf("%v", map[string]Secret{"c": s}),
		"wrapped err": fmt.Errorf("connect failed with %v", s).Error(),
	}
	for how, got := range renders {
		if strings.Contains(got, tokenValue) {
			t.Errorf("%s leaked the value: %s", how, got)
		}
		if !strings.Contains(got, "redacted") {
			t.Errorf("%s = %q, want a redaction marker so a reader knows something was withheld", how, got)
		}
	}
}

// TestSecretRefusesSerialization: marshalling fails rather than emitting a placeholder. A placeholder
// would be written to the secret store and injected into a task definition, producing a Collector that
// authenticates with the literal string "<redacted>" and reports as "deployed but never connected" —
// which is a far more expensive failure than an error at the call site.
func TestSecretRefusesSerialization(t *testing.T) {
	s := NewSecret(tokenValue)

	if _, err := s.MarshalJSON(); !errors.Is(err, ErrCredentialSerialized) {
		t.Errorf("MarshalJSON err = %v, want ErrCredentialSerialized", err)
	}
	if _, err := s.MarshalText(); !errors.Is(err, ErrCredentialSerialized) {
		t.Errorf("MarshalText err = %v, want ErrCredentialSerialized", err)
	}

	// The realistic accident: a Secret reaches a structure somebody serializes into a definition, a
	// report or a log line. It must fail, and the failure must not carry the value.
	for name, v := range map[string]any{
		"bare":         s,
		"struct field": struct{ Credential Secret }{s},
		"map value":    map[string]Secret{"authorization": s},
		"slice":        []Secret{s},
	} {
		b, err := json.Marshal(v)
		if err == nil {
			t.Errorf("%s: json.Marshal succeeded and produced %s", name, b)
			continue
		}
		if strings.Contains(err.Error(), tokenValue) {
			t.Errorf("%s: the marshal error itself leaked the value: %v", name, err)
		}
	}
}

// TestRevealIsTheOnlyWayOut: the value must still be retrievable, or the type is useless. Reveal is the
// single audited path and is named to be conspicuous in a diff.
func TestRevealIsTheOnlyWayOut(t *testing.T) {
	s := NewSecret(tokenValue)
	if s.Reveal() != tokenValue {
		t.Error("Reveal did not return the value")
	}
	if s.Empty() {
		t.Error("Empty = true for a populated secret")
	}
	if !NewSecret("").Empty() {
		t.Error("Empty = false for an unset secret")
	}
}

// TestHashAndFingerprint: the hash is the only representation persisted, and the fingerprint is what a
// log line carries instead of the reflex to log the value.
func TestHashAndFingerprint(t *testing.T) {
	s := NewSecret(tokenValue)
	h, err := s.Hash()
	if err != nil {
		t.Fatalf("Hash of a populated secret failed: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("Hash length = %d, want a 64-character SHA-256 hex digest", len(h))
	}
	if strings.Contains(h, tokenValue) {
		t.Error("the hash contains the value")
	}
	if s.Fingerprint() != h[:8] {
		t.Errorf("Fingerprint = %q, want the first 8 of %q", s.Fingerprint(), h)
	}
	if FingerprintOfHash(h) != s.Fingerprint() {
		t.Error("a stored hash does not fingerprint to the same value logged at issue time")
	}
	// Short or absent hashes must not panic — a row written before this column existed reads as "".
	if got := FingerprintOfHash(""); got != "" {
		t.Errorf("FingerprintOfHash(\"\") = %q", got)
	}
	other, err := NewSecret("other").Hash()
	if err != nil {
		t.Fatal(err)
	}
	if other == h {
		t.Error("two different values hash the same")
	}
}

func binding() Binding {
	return Binding{
		CollectorID:  "col-1",
		ConnectorUID: "uid-1",
		Provider:     "aws",
		AccountID:    "123456789012",
		Region:       "eu-west-1",
		NetworkID:    "vpc-0abc",
	}
}

// TestBindingMatches is the connection-time identity verification. The row that carries the design is the recreated
// connector: a credential presenting the right Collector id under a different connector UID must be
// REFUSED, not re-bound, or a dead connector's Collector attaches itself to the replacement.
func TestBindingMatches(t *testing.T) {
	b := binding()
	cases := []struct {
		name      string
		presented Binding
		want      bool
	}{
		{"identical", binding(), true},
		{"recreated connector: new UID", func() Binding { p := binding(); p.ConnectorUID = "uid-2"; return p }(), false},
		{"another Collector on the same connector", func() Binding { p := binding(); p.CollectorID = "col-2"; return p }(), false},
		{"another account", func() Binding { p := binding(); p.AccountID = "999999999999"; return p }(), false},
		{"another region", func() Binding { p := binding(); p.Region = "us-east-1"; return p }(), false},
		{"another network", func() Binding { p := binding(); p.NetworkID = "vpc-0def"; return p }(), false},
		{"another provider", func() Binding { p := binding(); p.Provider = "azure"; return p }(), false},
		{"incomplete", Binding{CollectorID: "col-1"}, false},
	}
	for _, tc := range cases {
		if got := b.Matches(tc.presented); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
	// An incomplete binding authenticates more than it should, so it authenticates nothing.
	if (Binding{}).Matches(Binding{}) {
		t.Error("two empty bindings matched")
	}
	if !b.Complete() {
		t.Error("a fully populated binding reads as incomplete")
	}
}

// TestRevokedIsTerminal: the tempting shortcut is un-revoking after a compromise, because it is so much
// smaller than issuing and redeploying. It is also handing the credential back to whoever took it.
func TestRevokedIsTerminal(t *testing.T) {
	for _, to := range []State{StatePending, StateActive, StateSuperseded} {
		err := Transition(StateRevoked, to)
		if err == nil {
			t.Errorf("revoked -> %s was allowed", to)
			continue
		}
		if !strings.Contains(err.Error(), "never reinstated") {
			t.Errorf("revoked -> %s: %v, want the reason spelled out", to, err)
		}
	}
}

// TestTransition pins the rest of the state machine.
func TestTransition(t *testing.T) {
	legal := []struct{ from, to State }{
		{StatePending, StateActive},
		{StatePending, StateRevoked},
		{StateActive, StateSuperseded},
		{StateActive, StateRevoked},
		{StateSuperseded, StateRevoked},
	}
	for _, c := range legal {
		if err := Transition(c.from, c.to); err != nil {
			t.Errorf("%s -> %s refused: %v", c.from, c.to, err)
		}
	}
	illegal := []struct{ from, to State }{
		{StateSuperseded, StateActive},
		{StateActive, StatePending},
		{StateSuperseded, StatePending},
	}
	for _, c := range illegal {
		if err := Transition(c.from, c.to); err == nil {
			t.Errorf("%s -> %s was allowed", c.from, c.to)
		}
	}
}

// TestSupersededStaysLive: the rotation window is the reason this state exists. A superseded credential
// the gateway stopped accepting would disconnect the old tasks the window is there to protect.
func TestSupersededStaysLive(t *testing.T) {
	live := map[State]bool{
		StatePending:    false,
		StateActive:     true,
		StateSuperseded: true,
		StateRevoked:    false,
	}
	for state, want := range live {
		if got := (Record{State: state}).Live(); got != want {
			t.Errorf("state %s: Live = %v, want %v", state, got, want)
		}
	}
}

// TestRecordKeepsNoValue: the persisted shape must have nowhere to put a credential. If a Secret field
// ever appears on Record it will reach the database, and the hash stops being the only thing the
// connector holds that is worth stealing.
func TestRecordKeepsNoValue(t *testing.T) {
	h, err := NewSecret(tokenValue).Hash()
	if err != nil {
		t.Fatal(err)
	}
	r := Record{TokenSHA256: h}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("a Record must be serializable — it is a database row: %v", err)
	}
	if strings.Contains(string(b), tokenValue) {
		t.Errorf("a serialized Record carried the value: %s", b)
	}
}

// TestHashRefusesAnEmptyCredential is the counterpart to TestSecretRefusesSerialization, and it exists
// for the same reason: the danger is a plausible value reaching the store, not an ugly one.
//
// sha256("") is a well-formed 64-character digest. Persisted as token_sha256 it reads as a successfully
// issued credential — Record.Live() returns true — every failed mint produces the identical value so
// they collide with one another, and it is the most widely published SHA-256 in existence. A mint that
// returned nothing, a config field left unset, an error swallowed upstream: each arrives here with an
// empty Secret, and hashing it silently is what turns a failed issuance into a recorded one.
func TestHashRefusesAnEmptyCredential(t *testing.T) {
	for name, s := range map[string]Secret{
		"explicitly empty": NewSecret(""),
		"zero value":       {},
	} {
		got, err := s.Hash()
		if !errors.Is(err, ErrEmptyCredential) {
			t.Errorf("%s: Hash err = %v, want ErrEmptyCredential", name, err)
		}
		if got != "" {
			t.Errorf("%s: Hash returned %q alongside its error; a caller ignoring err would persist it", name, got)
		}
		if got == EmptyDigest {
			t.Errorf("%s: Hash produced the empty digest", name)
		}
	}

	// The value it refuses to produce is exactly the one named, so the constant cannot drift from the
	// thing it is guarding against.
	sum := sha256.Sum256(nil)
	if hex.EncodeToString(sum[:]) != EmptyDigest {
		t.Errorf("EmptyDigest = %q, want sha256 of the empty string", EmptyDigest)
	}
}

// TestFingerprintOfAnEmptyCredentialIsConspicuous: a fingerprint is only ever read by a human working
// out what went wrong, so it must not render a broken row as eight plausible hex characters. Unlike
// Hash it does not fail — it is never persisted or compared, so a placeholder is right here and wrong
// there.
func TestFingerprintOfAnEmptyCredentialIsConspicuous(t *testing.T) {
	if got := NewSecret("").Fingerprint(); got != "<none>" {
		t.Errorf("Fingerprint of an empty secret = %q, want a conspicuous placeholder", got)
	}
	// A row already carrying the empty digest — written before this guard, or by some other path that
	// computed the hash itself — must read the same way rather than as a real credential.
	if got := FingerprintOfHash(EmptyDigest); got != "<none>" {
		t.Errorf("FingerprintOfHash(EmptyDigest) = %q, want a conspicuous placeholder", got)
	}
	// A real credential still fingerprints normally.
	h, err := NewSecret(tokenValue).Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got := FingerprintOfHash(h); got != h[:8] || got == "<none>" {
		t.Errorf("FingerprintOfHash(real) = %q, want %q", got, h[:8])
	}
}
