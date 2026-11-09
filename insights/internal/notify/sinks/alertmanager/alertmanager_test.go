package alertmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/notify"
	"github.com/odigos-io/odigos/insights/internal/notify/sinktest"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestContract(t *testing.T) {
	sinktest.Run(t, sinktest.Case{
		Descriptor: Descriptor,
		Factory:    New,
		Config: func(serverURL string) map[string]any {
			return map[string]any{"url": serverURL, "username": "u", "password": "p", "extra_labels": "team=platform"}
		},
	})
}

func TestPayload(t *testing.T) {
	var got []alert
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, err := notify.ValidateConfig(Descriptor.Fields, json.RawMessage(`{"url":"`+srv.URL+`/","username":"u","password":"p","extra_labels":"team=platform"}`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, notify.Deps{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	events := sinktest.Events()
	events[1].Action = repo.DispatchResolved
	before := time.Now()
	if res := s.Deliver(context.Background(), events, nil); res[0].Err != nil {
		t.Fatalf("deliver: %v", res[0].Err)
	}
	if gotPath != "/api/v2/alerts" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth == "" {
		t.Error("missing basic auth header")
	}
	if len(got) != 2 {
		t.Fatalf("alerts = %d", len(got))
	}
	a := got[0]
	if a.Labels["alertname"] != "OdigosInsightsAnomaly" || a.Labels["finding_key"] != events[0].Key || a.Labels["team"] != "platform" {
		t.Errorf("labels = %v", a.Labels)
	}
	if a.Labels["severity"] != "critical" || a.Labels["transaction_id"] != "42" {
		t.Errorf("labels = %v", a.Labels)
	}
	if a.Annotations["trace_id"] != "abc123" || a.Annotations["description"] == "" {
		t.Errorf("annotations = %v", a.Annotations)
	}
	if !a.EndsAt.After(before.Add(firingHold - time.Hour)) {
		t.Errorf("firing endsAt = %v, want ~now+%v", a.EndsAt, firingHold)
	}
	r := got[1]
	if r.Labels["alertname"] != "OdigosInsightsViolation" || r.Labels["rule"] != "allowed_callers" {
		t.Errorf("violation labels = %v", r.Labels)
	}
	if r.EndsAt.After(time.Now().Add(time.Second)) {
		t.Errorf("resolved endsAt = %v, want <= now", r.EndsAt)
	}
}
