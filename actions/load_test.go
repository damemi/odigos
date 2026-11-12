package actions

import "testing"

// Load parses the embedded catalog once: a later call returns the same catalog instead of
// re-reading every file.
func TestLoadParsesOnce(t *testing.T) {
	if err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := Get()
	if err := Load(); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	second := Get()
	if len(first) == 0 || len(first) != len(second) || &first[0] != &second[0] {
		t.Fatal("the second Load parsed the catalog again")
	}
}
