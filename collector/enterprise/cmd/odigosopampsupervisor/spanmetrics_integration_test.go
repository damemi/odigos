package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/commander"
	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/config"
	"go.uber.org/zap"
)

// Uses a real Collector when explicitly supplied. Runs entirely on loopback with synthetic spans;
// no customer endpoints, credentials, AWS resources or new Collector image builds are needed.
func TestSpanMetricsUpdateExportsOriginalTracesAndDerivedMetrics(t *testing.T) {
	executable := os.Getenv("ODIGOS_TEST_COLLECTOR")
	if executable == "" {
		t.Skip("set ODIGOS_TEST_COLLECTOR to run against a real Collector binary")
	}
	traces, metrics := make(chan struct{}, 1), make(chan struct{}, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/traces" && strings.Contains(string(body), "after-spanmetrics") {
			select {
			case traces <- struct{}{}:
			default:
			}
		}
		if r.URL.Path == "/v1/metrics" && strings.Contains(string(body), "traces.span.metrics.calls") {
			select {
			case metrics <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer sink.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	load := func(name string) []byte {
		document, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		replacer := strings.NewReplacer("127.0.0.1:14318", address, "debug: {}", "otlp_http/test:\n    endpoint: "+sink.URL+"\n    encoding: json\n    compression: none", "[debug]", "[otlp_http/test]")
		return []byte(replacer.Replace(string(document)))
	}
	dir := t.TempDir()
	effective, candidate, logfile := filepath.Join(dir, "effective.yaml"), filepath.Join(dir, "candidate.yaml"), filepath.Join(dir, "collector.log")
	before, after := load("spanmetrics-before.yaml"), load("spanmetrics-after.yaml")
	if err := os.WriteFile(effective, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, after, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := commander.NewCommander(zap.NewNop(), logfile, config.Agent{Executable: executable}, "--config", effective)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Stop(ctx)
		if t.Failed() {
			if body, err := os.ReadFile(logfile); err == nil {
				t.Log(string(body))
			}
		}
	})
	client := &http.Client{Timeout: 2 * time.Second}
	send := func(name string) {
		start := time.Now().Add(-time.Millisecond).UnixNano()
		body := fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"spanmetrics-regression"}}]},"scopeSpans":[{"scope":{"name":"regression"},"spans":[{"traceId":"0123456789abcdef0123456789abcdef","spanId":"0123456789abcdef","name":%q,"kind":2,"startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`, name, start, start+1000000)
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			res, err := client.Post("http://"+address+"/v1/traces", "application/json", strings.NewReader(body))
			if err == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("Collector did not accept the synthetic trace")
	}
	send("before-spanmetrics")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.ValidateConfig(ctx, candidate); err != nil {
		t.Fatalf("spanmetrics update rejected: %v", err)
	}
	if err := c.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(effective, after, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	send("after-spanmetrics")
	for name, ch := range map[string]<-chan struct{}{"original traces": traces, "derived span metrics": metrics} {
		select {
		case <-ch:
		case <-time.After(15 * time.Second):
			t.Fatalf("Collector did not export %s", name)
		}
	}
}
