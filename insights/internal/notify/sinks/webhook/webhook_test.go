package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/notify"
	"github.com/odigos-io/odigos/insights/internal/notify/sinktest"
)

func TestContract(t *testing.T) {
	sinktest.Run(t, sinktest.Case{
		Descriptor: Descriptor,
		Factory:    New,
		Config: func(serverURL string) map[string]any {
			return map[string]any{"url": serverURL, "secret": "s3cret", "headers": "X-Team=platform"}
		},
	})
}

func TestSignatureAndHeaders(t *testing.T) {
	var body []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		hdr = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg, err := notify.ValidateConfig(Descriptor.Fields, json.RawMessage(`{"url":"`+srv.URL+`","secret":"s3cret","headers":"X-Team=platform, Authorization=Bearer t"}`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, notify.Deps{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ev := sinktest.Events()[0]
	res := s.Deliver(context.Background(), []notify.Event{ev}, nil)
	if res[0].Err != nil {
		t.Fatalf("deliver: %v", res[0].Err)
	}
	if got, want := hdr.Get(HeaderSignature), Sign([]byte("s3cret"), body); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
	if hdr.Get(HeaderEventID) != ev.ID() || hdr.Get(HeaderEventType) != "finding.firing" {
		t.Errorf("event headers = %q %q", hdr.Get(HeaderEventID), hdr.Get(HeaderEventType))
	}
	if hdr.Get("X-Team") != "platform" || hdr.Get("Authorization") != "Bearer t" {
		t.Errorf("extra headers missing: %v", hdr)
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.EventID != ev.ID() || p.Finding.Key != ev.Key || p.Finding.Severity != "critical" || p.Finding.TransactionID != 42 {
		t.Errorf("payload = %+v", p)
	}
}
