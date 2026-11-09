package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/notify"
	"github.com/odigos-io/odigos/insights/internal/notify/sinktest"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestContract(t *testing.T) {
	sinktest.Run(t, sinktest.Case{
		Descriptor: Descriptor,
		Factory:    New,
		Config: func(serverURL string) map[string]any {
			return map[string]any{"webhook_url": serverURL, "mention": "<!channel>"}
		},
	})
}

func TestRender(t *testing.T) {
	var got message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, err := notify.ValidateConfig(Descriptor.Fields, json.RawMessage(`{"webhook_url":"`+srv.URL+`","mention":"<!channel>"}`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, notify.Deps{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ev := sinktest.Events()[0]
	if res := s.Deliver(context.Background(), []notify.Event{ev}, nil); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if len(got.Blocks) < 3 || !strings.HasPrefix(got.Blocks[0].Text.Text, "<!channel>") {
		t.Errorf("critical message should lead with mention: %+v", got.Blocks)
	}
	if !strings.Contains(got.Text, "CRITICAL") || !strings.Contains(got.Text, "shop/checkout") {
		t.Errorf("fallback text = %q", got.Text)
	}

	ev.Action = repo.DispatchResolved
	ev.Status = "accepted"
	if res := s.Deliver(context.Background(), []notify.Event{ev}, nil); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if len(got.Blocks) != 1 || !strings.Contains(got.Text, "resolved") {
		t.Errorf("resolved message = %+v", got)
	}
}

func TestRejectsBadURL(t *testing.T) {
	cfg, err := notify.ValidateConfig(Descriptor.Fields, json.RawMessage(`{"webhook_url":"not a url"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, notify.DefaultDeps()); err == nil {
		t.Fatal("expected error for malformed webhook url")
	}
}
