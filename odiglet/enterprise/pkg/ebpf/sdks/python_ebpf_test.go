package sdks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/api/agentsignalconfig"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	commonapisampling "github.com/odigos-io/odigos/common/api/sampling"
	"github.com/odigos-io/odigos/common/configprovider"
	"github.com/odigos-io/odigos/instrumentation"
	"go.opentelemetry.io/collector/consumer/consumertest"

	coreconfig "github.com/odigos-io/ebpf-core/pkg/config"
	pyebpf "github.com/odigos-io/python-ebpf"
)

// TestPythonEbpfConfigMapping pins the CR → python-ebpf translation. A rule that never reaches the
// kernel exports 100% of a workload the user asked to sample.
func TestPythonEbpfConfigMapping(t *testing.T) {
	if _, err := pythonEbpfConfig("not-a-config"); err == nil {
		t.Fatal("wrong type must error rather than apply an empty policy")
	}

	for name, conf := range map[string]any{
		"nil config": nil,
		"nil traces": &odigosv1.ContainerAgentConfig{ContainerName: "app"},
	} {
		got, err := pythonEbpfConfig(conf)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.HeadSampling != nil {
			t.Errorf("%s must not invent a sampling policy, got %#v", name, got.HeadSampling)
		}
		if got.SpanMetrics != nil {
			t.Errorf("%s must leave span metrics off, got %#v", name, got.SpanMetrics)
		}
		if !stringSlicesEqual(got.WatchedQualnames, pyebpf.DefaultWatchedQualnames) {
			t.Errorf("%s: watch set = %v, want the built-in set (an empty one traces nothing)", name, got.WatchedQualnames)
		}
	}

	got, err := pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			HeadSampling: &commonapisampling.HeadSamplingConfig{
				DryRun:          true,
				SpanMetricsMode: commonapisampling.SpanMetricsModeAllSpans,
				NoisyOperations: []commonapisampling.NoisyOperation{
					{
						Id:               "http-server",
						Name:             "checkout",
						PercentageAtMost: float64ptr(5),
						Operation: &commonapisampling.HeadSamplingOperationMatcher{
							HttpServer: &commonapisampling.HeadSamplingHttpServerOperationMatcher{
								Route:       "/checkout",
								RoutePrefix: "/api",
								Method:      "POST",
							},
						},
					},
					{
						Id:               "http-client",
						Disabled:         true,
						PercentageAtMost: float64ptr(0),
						Operation: &commonapisampling.HeadSamplingOperationMatcher{
							HttpClient: &commonapisampling.HeadSamplingHttpClientOperationMatcher{
								ServerAddress:       "payments.internal",
								TemplatedPath:       "/charge/{id}",
								TemplatedPathPrefix: "/charge",
								Method:              "PUT",
							},
						},
					},
					{Id: "match-all", PercentageAtMost: float64ptr(1)},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	hs := got.HeadSampling
	if hs == nil || !hs.DryRun || hs.SpanMetricsMode != coreconfig.SpanMetricsModeAllSpans || len(hs.NoisyOperations) != 3 {
		t.Fatalf("head sampling = %#v", hs)
	}
	server, client, all := hs.NoisyOperations[0], hs.NoisyOperations[1], hs.NoisyOperations[2]
	if server.ID != "http-server" || server.Name != "checkout" || *server.PercentageAtMost != 5 ||
		server.Operation == nil || server.Operation.HTTPClient != nil ||
		*server.Operation.HTTPServer != (coreconfig.HeadSamplingHTTPServerMatcher{Route: "/checkout", RoutePrefix: "/api", Method: "POST"}) {
		t.Errorf("server rule = %#v", server)
	}
	if client.ID != "http-client" || !client.Disabled || *client.PercentageAtMost != 0 ||
		client.Operation == nil || client.Operation.HTTPServer != nil ||
		*client.Operation.HTTPClient != (coreconfig.HeadSamplingHTTPClientMatcher{
			ServerAddress: "payments.internal", TemplatedPath: "/charge/{id}", TemplatedPathPrefix: "/charge", Method: "PUT"}) {
		t.Errorf("client rule = %#v", client)
	}
	// A nil operation is ebpf-core's match-all rule; an empty matcher would match nothing.
	if all.ID != "match-all" || all.Operation != nil || *all.PercentageAtMost != 1 {
		t.Errorf("match-all rule = %#v", all)
	}
}

// TestPythonEbpfSpanMetricsMapping pins that the container's span-metrics config reaches python-ebpf
// as is: python-ebpf fills Odigos's defaults for a zero field itself.
func TestPythonEbpfSpanMetricsMapping(t *testing.T) {
	got, err := pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Metrics: &agentsignalconfig.AgentMetricsConfig{
			SpanMetrics: &agentsignalconfig.AgentSpanMetricsConfig{
				Dimensions:         []string{"http.route", "http.response.status_code"},
				IntervalMs:         2000,
				HistogramBucketsMs: []int{0, 5, 100},
			},
		},
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	want := pyebpf.SpanMetricsConfig{
		Dimensions:         []string{"http.route", "http.response.status_code"},
		IntervalMs:         2000,
		HistogramBucketsMs: []int{0, 5, 100},
	}
	if got.SpanMetrics == nil || !reflect.DeepEqual(*got.SpanMetrics, want) {
		t.Errorf("span metrics = %#v, want %#v", got.SpanMetrics, want)
	}
	if got.HeadSampling != nil {
		t.Errorf("head sampling = %#v, want nil without traces config", got.HeadSampling)
	}

	// Metrics without span metrics (runtime metrics only) leaves them off.
	got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{Metrics: &agentsignalconfig.AgentMetricsConfig{}})
	if err != nil || got.SpanMetrics != nil {
		t.Errorf("span metrics = %#v, %v; want off", got.SpanMetrics, err)
	}
}

// TestPythonEbpfHeadersMapping pins that the headers-collection rules' keys reach python-ebpf as its
// server request headers, and that no rule, or one with no keys, records none.
func TestPythonEbpfHeadersMapping(t *testing.T) {
	got, err := pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			HeadersCollection: &instrumentationrules.HttpHeadersCollection{HeaderKeys: []string{"X-Request-Id", "user-agent"}},
		},
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	want := pyebpf.HTTPHeaders{RequestKeys: []string{"X-Request-Id", "user-agent"}}
	if got.HTTPHeaders == nil || !reflect.DeepEqual(*got.HTTPHeaders, want) {
		t.Errorf("headers = %#v, want %#v", got.HTTPHeaders, want)
	}

	for _, hc := range []*instrumentationrules.HttpHeadersCollection{nil, {}} {
		got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
			Traces: &agentsignalconfig.AgentTracesConfig{HeadersCollection: hc},
		})
		if err != nil || got.HTTPHeaders != nil {
			t.Errorf("rule %#v: headers = %#v, %v; want nil", hc, got.HTTPHeaders, err)
		}
	}
}

// TestPythonEbpfMessagingMapping pins that the messaging payload rule reaches python-ebpf with its
// limits, that unset limits are no limit and cutting, and that no rule records no message body.
func TestPythonEbpfMessagingMapping(t *testing.T) {
	maxLen, drop := int64(512), true
	got, err := pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			PayloadCollection: &instrumentationrules.PayloadCollection{
				Messaging: &instrumentationrules.MessagingPayloadCollection{MaxPayloadLength: &maxLen, DropPartialPayloads: &drop},
			},
		},
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	want := pyebpf.MessagingPayload{MaxPayloadLength: 512, DropPartialPayloads: true}
	if got.MessagingPayload == nil || *got.MessagingPayload != want {
		t.Errorf("messaging = %#v, want %#v", got.MessagingPayload, want)
	}

	got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			PayloadCollection: &instrumentationrules.PayloadCollection{Messaging: &instrumentationrules.MessagingPayloadCollection{}},
		},
	})
	if err != nil || got.MessagingPayload == nil || *got.MessagingPayload != (pyebpf.MessagingPayload{}) {
		t.Errorf("rule without limits: messaging = %#v, %v; want the zero rule", got.MessagingPayload, err)
	}

	for _, pc := range []*instrumentationrules.PayloadCollection{nil, {DbQuery: &instrumentationrules.DbQueryPayloadCollection{}}} {
		got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
			Traces: &agentsignalconfig.AgentTracesConfig{PayloadCollection: pc},
		})
		if err != nil || got.MessagingPayload != nil {
			t.Errorf("payload rules %#v: messaging = %#v, %v; want nil", pc, got.MessagingPayload, err)
		}
	}
}

// TestPythonEbpfDBQueryMapping pins that the DB query payload rule reaches python-ebpf with its
// limits, that unset limits are no limit and cutting, and that no rule records no db.query.text.
func TestPythonEbpfDBQueryMapping(t *testing.T) {
	maxLen, drop := int64(2048), true
	got, err := pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			PayloadCollection: &instrumentationrules.PayloadCollection{
				DbQuery: &instrumentationrules.DbQueryPayloadCollection{MaxPayloadLength: &maxLen, DropPartialPayloads: &drop},
			},
		},
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	want := pyebpf.DBQueryPayload{MaxPayloadLength: 2048, DropPartialPayloads: true}
	if got.DBQueryPayload == nil || *got.DBQueryPayload != want {
		t.Errorf("DB queries = %#v, want %#v", got.DBQueryPayload, want)
	}

	got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			PayloadCollection: &instrumentationrules.PayloadCollection{DbQuery: &instrumentationrules.DbQueryPayloadCollection{}},
		},
	})
	if err != nil || got.DBQueryPayload == nil || *got.DBQueryPayload != (pyebpf.DBQueryPayload{}) {
		t.Errorf("rule without limits: DB queries = %#v, %v; want the zero rule", got.DBQueryPayload, err)
	}

	for _, pc := range []*instrumentationrules.PayloadCollection{nil, {Messaging: &instrumentationrules.MessagingPayloadCollection{}}} {
		got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
			Traces: &agentsignalconfig.AgentTracesConfig{PayloadCollection: pc},
		})
		if err != nil || got.DBQueryPayload != nil {
			t.Errorf("payload rules %#v: DB queries = %#v, %v; want nil", pc, got.DBQueryPayload, err)
		}
	}
}

// TestPythonEbpfTraceVerbosityMapping pins that the trace-verbosity rules' library names reach
// python-ebpf as is, and that no rule traces every library.
func TestPythonEbpfTraceVerbosityMapping(t *testing.T) {
	got, err := pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			TraceVerbosity: &instrumentationrules.TraceVerbosity{
				DisabledLibraries: []instrumentationrules.InstrumentationLibrary{
					{Language: common.PythonProgrammingLanguage, LibraryName: "requests"},
					{Language: common.PythonProgrammingLanguage, LibraryName: "github.com/odigos-io/python-ebpf/redis"},
				},
				EnabledLibraries: []instrumentationrules.InstrumentationLibrary{
					{Language: common.PythonProgrammingLanguage, LibraryName: "redis"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	want := pyebpf.TraceVerbosity{
		DisabledLibraries: []string{"requests", "github.com/odigos-io/python-ebpf/redis"},
		EnabledLibraries:  []string{"redis"},
	}
	if got.TraceVerbosity == nil || !reflect.DeepEqual(*got.TraceVerbosity, want) {
		t.Errorf("trace verbosity = %#v, want %#v", got.TraceVerbosity, want)
	}

	got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{Traces: &agentsignalconfig.AgentTracesConfig{}})
	if err != nil || got.TraceVerbosity != nil {
		t.Errorf("trace verbosity = %#v, %v; want nil without a rule", got.TraceVerbosity, err)
	}
}

// TestPythonEbpfFactoryNeedsConsumers pins that the factory refuses a missing consumer, which would
// otherwise fail only once a Python process is instrumented.
func TestPythonEbpfFactoryNeedsConsumers(t *testing.T) {
	if _, err := NewPythonEbpfInstrumentationFactoryEnterprise(nil, consumertest.NewNop(), false); err == nil {
		t.Error("nil traces consumer must error")
	}
	if _, err := NewPythonEbpfInstrumentationFactoryEnterprise(consumertest.NewNop(), nil, false); err == nil {
		t.Error("nil metrics consumer must error")
	}
	if _, err := NewPythonEbpfInstrumentationFactoryEnterprise(consumertest.NewNop(), consumertest.NewNop(), false); err != nil {
		t.Errorf("both consumers: %v", err)
	}
}

// TestPythonEbpfFactoryKeepsWaspOptOut pins that the odiglet's -wasp-disabled reaches the factory,
// which passes it to every instrumentation it creates.
func TestPythonEbpfFactoryKeepsWaspOptOut(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		f, err := NewPythonEbpfInstrumentationFactoryEnterprise(consumertest.NewNop(), consumertest.NewNop(), disabled)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.(*PythonEbpfInstrumentationFactoryEnterprise).waspDisabled; got != disabled {
			t.Errorf("waspDisabled = %v, want %v", got, disabled)
		}
	}
}

// TestPythonEbpfRefusesProcessWithOtherAgent pins the switch to python-ebpf on a running workload:
// pods that started under another Python distro carry its in-process agent until they restart, and
// python-ebpf must not trace them a second time. Pods stamped python-ebpf, and processes that started
// before the workload was instrumented (no stamp), are attached.
func TestPythonEbpfRefusesProcessWithOtherAgent(t *testing.T) {
	procDir := t.TempDir()
	writeEnviron := func(pid int, env ...string) {
		dir := filepath.Join(procDir, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var b []byte
		for _, kv := range env {
			b = append(append(b, kv...), 0)
		}
		if err := os.WriteFile(filepath.Join(dir, "environ"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeEnviron(101, "PATH=/usr/bin", "ODIGOS_DISTRO_NAME=python-enterprise", "PYTHONPATH=:/var/odigos/python")
	writeEnviron(102, "ODIGOS_DISTRO_NAME=python-enterprise3.8")
	writeEnviron(103, "PATH=/usr/bin", "ODIGOS_DISTRO_NAME=python-ebpf")
	writeEnviron(104, "PATH=/usr/bin", "MY_ODIGOS_DISTRO_NAME=python-enterprise")
	writeEnviron(105, "ODIGOS_DISTRO_NAME=")
	writeEnviron(106) // a process that cleared its environment
	// 107 has no /proc entry: the process is gone, and python-ebpf's own attach reports that.

	fi, err := NewPythonEbpfInstrumentationFactoryEnterprise(consumertest.NewNop(), consumertest.NewNop(), false)
	if err != nil {
		t.Fatal(err)
	}
	f := fi.(*PythonEbpfInstrumentationFactoryEnterprise)
	f.procDir = procDir

	for pid, refused := range map[int]bool{101: true, 102: true, 103: false, 104: false, 105: false, 106: false, 107: false} {
		inst, err := f.CreateInstrumentation(context.Background(), pid, instrumentation.Settings{ServiceName: "svc"})
		if got := errors.Is(err, ErrRunsOtherPythonAgent); got != refused {
			t.Errorf("pid %d: CreateInstrumentation err = %v, refused = %v, want %v", pid, err, got, refused)
		}
		if refused {
			if inst != nil {
				t.Errorf("pid %d: refused but returned an instrumentation", pid)
			}
			continue
		}
		if err != nil || inst == nil {
			t.Errorf("pid %d: CreateInstrumentation = %v, %v; want an instrumentation", pid, inst, err)
		}
	}
}

// TestPythonEbpfApplyConfig pins that a control-plane update reaches python-ebpf's provider, which
// it applies from the next trace; ApplyConfig used to drop it.
func TestPythonEbpfApplyConfig(t *testing.T) {
	cp := configprovider.NewConfigProvider(pyebpf.Config{WatchedQualnames: pyebpf.DefaultWatchedQualnames})
	w := &pythonEbpfInstrumentation{cp: cp}
	got := make(chan pyebpf.Config, 1)
	go func() { got <- <-cp.Watch() }()

	err := w.ApplyConfig(context.Background(), &odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			HeadSampling: &commonapisampling.HeadSamplingConfig{
				NoisyOperations: []commonapisampling.NoisyOperation{{Id: "all", PercentageAtMost: float64ptr(25)}},
			},
		},
		Metrics: &agentsignalconfig.AgentMetricsConfig{
			SpanMetrics: &agentsignalconfig.AgentSpanMetricsConfig{IntervalMs: 5000},
		},
	})
	if err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	select {
	case cfg := <-got:
		if cfg.HeadSampling == nil || len(cfg.HeadSampling.NoisyOperations) != 1 ||
			*cfg.HeadSampling.NoisyOperations[0].PercentageAtMost != 25 {
			t.Errorf("sent head sampling = %#v", cfg.HeadSampling)
		}
		if cfg.SpanMetrics == nil || cfg.SpanMetrics.IntervalMs != 5000 {
			t.Errorf("sent span metrics = %#v", cfg.SpanMetrics)
		}
	case <-time.After(time.Second):
		t.Fatal("update never reached the provider")
	}

	if err := w.ApplyConfig(context.Background(), "not-a-config"); err == nil {
		t.Error("wrong type must error rather than clear the policy")
	}
}

// TestPythonEbpfProbesMapping pins that the Python probes of the custom instrumentation rules reach
// python-ebpf with their captures, and that no probes is none.
func TestPythonEbpfProbesMapping(t *testing.T) {
	got, err := pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			CustomInstrumentations: &instrumentationrules.CustomInstrumentations{
				Python: []instrumentationrules.PythonCustomProbe{
					{Module: "app.billing", Qualname: "Invoice.calculate", Captures: []instrumentationrules.PythonCapture{
						{Path: "arg.order.total"}, {Path: "arg.user_id", Mode: instrumentationrules.PythonCaptureValue},
					}},
					{Module: "__main__", Qualname: "handler"},
				},
				Java: []instrumentationrules.JavaCustomProbe{{ClassName: "com.acme.Pricing", MethodName: "calculate"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	want := []pyebpf.Probe{
		{Module: "app.billing", Qualname: "Invoice.calculate", Captures: []pyebpf.Capture{
			{Path: "arg.order.total"}, {Path: "arg.user_id", Mode: pyebpf.CaptureValue},
		}},
		{Module: "__main__", Qualname: "handler"},
	}
	if !reflect.DeepEqual(got.Probes, want) {
		t.Errorf("probes = %v, want %v", got.Probes, want)
	}

	for _, ci := range []*instrumentationrules.CustomInstrumentations{nil, {}} {
		got, err = pythonEbpfConfig(&odigosv1.ContainerAgentConfig{
			Traces: &agentsignalconfig.AgentTracesConfig{CustomInstrumentations: ci},
		})
		if err != nil || got.Probes != nil {
			t.Errorf("rules %#v: probes = %v, %v; want none", ci, got.Probes, err)
		}
	}
}
