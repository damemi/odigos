package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/extension/extensiontest"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"

	managed "github.com/odigos-io/odigos/collector/extension/odigosconfigconnectorextension"
	sqlprocessor "github.com/odigos-io/odigos/collector/processor/odigossqlqueryprocessor"
	urlprocessor "github.com/odigos-io/odigos/collector/processor/odigosurltemplateprocessor"
)

type managedActionsHost struct {
	extensions map[component.ID]component.Component
}

func (h managedActionsHost) GetExtensions() map[component.ID]component.Component { return h.extensions }

func TestManagedActionScopesWithRealProcessors(t *testing.T) {
	ctx := context.Background()
	extID := component.NewIDWithName(managed.Type, "managed-actions")
	cfg := &managed.Config{
		CollectorConfig: map[string]any{"dbQueryTemplatization": map[string]any{"templatizeLiterals": true}},
		Workloads:       []managed.Workload{{ID: "checkout", ResourceAttributes: map[string]string{"service.name": "checkout", "cloud.region": "us-east-1"}}},
		Scopes: []managed.Scope{
			{WorkloadIDs: []string{"checkout"}, CollectorConfig: map[string]any{"inferDbAttributes": map[string]any{}}},
			{Languages: []string{"java"}, CollectorConfig: map[string]any{"dbQueryTemplatization": map[string]any{"removePostgresCastOperator": true}}},
			{Languages: []string{"java", "python", "nodejs"}, CollectorConfig: map[string]any{"urlTemplatization": map[string]any{"templatizationRules": []any{"/orders/{orderId}"}, "default": map[string]any{"disabled": true}}}},
		},
	}
	ext, err := managed.NewFactory().Create(ctx, extensiontest.NewNopSettings(managed.Type), cfg)
	require.NoError(t, err)
	host := managedActionsHost{extensions: map[component.ID]component.Component{extID: ext}}
	require.NoError(t, ext.Start(ctx, host))
	t.Cleanup(func() { require.NoError(t, ext.Shutdown(ctx)) })
	processorConfig := map[string]any{"odigos_config_extension": extID.String()}
	sink := &consumertest.TracesSink{}
	sqlFactory := sqlprocessor.NewFactory()
	sqlConfig := sqlFactory.CreateDefaultConfig()
	require.NoError(t, confmap.NewFromStringMap(processorConfig).Unmarshal(sqlConfig))
	sql, err := sqlFactory.CreateTraces(ctx, processortest.NewNopSettings(sqlFactory.Type()), sqlConfig, sink)
	require.NoError(t, err)
	require.NoError(t, sql.Start(ctx, host))
	t.Cleanup(func() { require.NoError(t, sql.Shutdown(ctx)) })
	urlFactory := urlprocessor.NewFactory()
	urlConfig := urlFactory.CreateDefaultConfig()
	require.NoError(t, confmap.NewFromStringMap(processorConfig).Unmarshal(urlConfig))
	url, err := urlFactory.CreateTraces(ctx, processortest.NewNopSettings(urlFactory.Type()), urlConfig, sql)
	require.NoError(t, err)
	require.NoError(t, url.Start(ctx, host))
	t.Cleanup(func() { require.NoError(t, url.Shutdown(ctx)) })

	cases := []struct {
		workload, language string
		infer, cast, route bool
	}{
		{"checkout", "java", true, true, true},
		{"checkout", "python", true, false, true},
		{"payments", "nodejs", false, false, true},
		{"unknown", "java", false, true, true},
		{"unknown", "ruby", false, false, false},
		{"checkout", "", true, false, false},
	}
	traces := ptrace.NewTraces()
	for _, tc := range cases {
		rs := traces.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", tc.workload)
		rs.Resource().Attributes().PutStr("cloud.region", "us-east-1")
		if tc.language != "" {
			rs.Resource().Attributes().PutStr("telemetry.sdk.language", tc.language)
		}
		spans := rs.ScopeSpans().AppendEmpty().Spans()
		http := spans.AppendEmpty()
		http.SetName("GET")
		http.SetKind(ptrace.SpanKindServer)
		http.Attributes().PutStr("http.request.method", "GET")
		http.Attributes().PutStr("url.path", "/orders/123")
		db := spans.AppendEmpty()
		db.SetName("query")
		db.SetKind(ptrace.SpanKindClient)
		db.Attributes().PutStr("db.system", "postgresql")
		db.Attributes().PutStr("db.query.text", "SELECT * FROM users WHERE id = 42::int")
	}
	require.NoError(t, url.ConsumeTraces(ctx, traces))
	require.Equal(t, len(cases)*2, sink.SpanCount(), "nonmatching scopes must not drop spans")
	out := sink.AllTraces()[0].ResourceSpans()
	for i, tc := range cases {
		t.Run(tc.workload+"/"+tc.language, func(t *testing.T) {
			spans := out.At(i).ScopeSpans().At(0).Spans()
			route, present := spans.At(0).Attributes().Get("http.route")
			require.Equal(t, tc.route, present)
			if present {
				require.Equal(t, "/orders/{orderId}", route.Str())
			}
			db := spans.At(1).Attributes()
			query, _ := db.Get("db.query.text")
			require.NotContains(t, query.Str(), "42", "global literal templatization must apply to every resource")
			if tc.cast {
				require.NotContains(t, query.Str(), "::")
			} else {
				require.Contains(t, strings.ReplaceAll(query.Str(), " ", ""), "::int")
			}
			operation, present := db.Get("db.operation.name")
			require.Equal(t, tc.infer, present)
			if present {
				require.Equal(t, "SELECT", operation.Str())
			}
		})
	}
}
