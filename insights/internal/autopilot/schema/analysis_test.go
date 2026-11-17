package schema

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/common/api/instrumentationrules"
)

func TestParseValidJava(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": "1",
		"transactionId": "GET /users",
		"recommendations": [{
			"category": "observability",
			"function": "com.example.UserService.getUser",
			"language": "java",
			"probe": {"className": "com.example.UserService", "methodName": "getUser"},
			"why": "This is the domain operation for the request."
		}]
	}`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.TransactionID != "GET /users" || len(got.Recommendations) != 1 {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestRecommendationStoresWhyCategoryAndAddedAt(t *testing.T) {
	before := time.Now().Add(-time.Second).UTC()
	rec := Recommendation{
		Category: CategoryPerformance,
		Function: "com.acme.Checkout.handle",
		Language: LangJava,
		Probe:    Probe{ClassName: "com.acme.Checkout", MethodName: "handle"},
		Why:      " seen on the checkout edge ",
	}
	ci, err := rec.ToCustomInstrumentations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ci.Java) != 1 {
		t.Fatalf("java probes: %d", len(ci.Java))
	}
	got := ci.Java[0]
	if got.Notes != "seen on the checkout edge" {
		t.Fatalf("notes: %q", got.Notes)
	}
	if got.Category != instrumentationrules.CustomProbeCategoryPerformance {
		t.Fatalf("category: %q", got.Category)
	}
	added, err := time.Parse(time.RFC3339, got.AddedAt)
	if err != nil {
		t.Fatal(err)
	}
	if added.Before(before) || added.After(time.Now().Add(time.Second).UTC()) {
		t.Fatalf("addedAt %s", got.AddedAt)
	}
}

func TestParseRejectsUnknownCategory(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": "1",
		"transactionId": "t",
		"recommendations": [{
			"category": "vibes",
			"function": "fn",
			"language": "java",
			"probe": {"className": "C", "methodName": "m"},
			"why": "nope"
		}]
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected category error")
	}
}

func TestParseRejectsTooManyPerCategory(t *testing.T) {
	recs := ""
	for i := 0; i < 6; i++ {
		if i > 0 {
			recs += ","
		}
		recs += `{"category":"security","function":"f","language":"java","probe":{"className":"C","methodName":"m"},"why":"x"}`
	}
	raw := []byte(`{"schemaVersion":"1","transactionId":"t","recommendations":[` + recs + `]}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected cap error")
	}
}

func TestParseRejectsWrongVersion(t *testing.T) {
	if _, err := Parse([]byte(`{"schemaVersion":"2","transactionId":"t","recommendations":[]}`)); err == nil {
		t.Fatal("expected version error")
	}
}
