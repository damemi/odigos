package splunk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/notify"
	"github.com/odigos-io/odigos-enterprise/insights/internal/notify/sinks/webhook"
	"github.com/odigos-io/odigos-enterprise/insights/internal/notify/sinktest"
)

func TestContract(t *testing.T) {
	sinktest.Run(t, sinktest.Case{
		Descriptor: Descriptor,
		Factory:    New,
		Config: func(serverURL string) map[string]any {
			return map[string]any{"url": serverURL, "token": "tok", "index": "odigos"}
		},
	})
}

func build(t *testing.T, raw string, client *http.Client) notify.Sink {
	t.Helper()
	cfg, err := notify.ValidateConfig(Descriptor.Fields, json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, notify.Deps{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeliverBatchesHECEvents(t *testing.T) {
	var path, auth string
	var got []hecEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		dec := json.NewDecoder(r.Body)
		for {
			var e hecEvent
			if err := dec.Decode(&e); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Errorf("decode: %v", err)
				break
			}
			got = append(got, e)
		}
		_, _ = w.Write([]byte(`{"text":"Success","code":0}`))
	}))
	defer srv.Close()

	s := build(t, `{"url":"`+srv.URL+`","token":"tok","index":"odigos"}`, srv.Client())
	events := sinktest.Events()
	for _, r := range s.Deliver(context.Background(), events, nil) {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	if path != eventPath || auth != "Splunk tok" {
		t.Errorf("path = %q, auth = %q", path, auth)
	}
	if len(got) != len(events) {
		t.Fatalf("got %d events in the batch, want %d", len(got), len(events))
	}
	e := got[0]
	if e.Index != "odigos" || e.SourceType != "_json" || e.Source != source || e.Time == 0 {
		t.Errorf("envelope = %+v", e)
	}
	raw, _ := json.Marshal(e.Event)
	var p webhook.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Finding.Key != events[0].Key || p.EventType != "finding.firing" {
		t.Errorf("event payload = %+v", p)
	}
}

func TestKeepsExplicitPath(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { path = r.URL.Path }))
	defer srv.Close()
	if err := build(t, `{"url":"`+srv.URL+`/proxy/hec","token":"tok"}`, srv.Client()).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if path != "/proxy/hec" {
		t.Errorf("path = %q, want the configured one", path)
	}
}

func TestInsecureSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	client := notify.DefaultDeps().HTTPClient

	if err := build(t, `{"url":"`+srv.URL+`","token":"tok"}`, client).Test(context.Background()); err == nil {
		t.Fatal("self-signed certificate was accepted without opting in")
	}
	if err := build(t, `{"url":"`+srv.URL+`","token":"tok","insecure_skip_verify":true}`, client).Test(context.Background()); err != nil {
		t.Fatalf("insecure_skip_verify: %v", err)
	}
	if tr, ok := client.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("shared client was modified")
	}
}
