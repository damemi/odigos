package odigosurltemplatizationexporter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	deprecatedsemconv "go.opentelemetry.io/collector/semconv/v1.18.0"
	semconv "go.opentelemetry.io/collector/semconv/v1.27.0"
	"go.uber.org/zap"

	"github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/collector"
	"github.com/odigos-io/odigos/common/odigosattributes"
)

type stubOdigosConfigExtension struct {
	component.Component
	key   string
	attrs pcommon.Map
	err   error
}

func (s *stubOdigosConfigExtension) GetFromResource(pcommon.Resource) (*api.SourceCollectorConfig, bool) {
	return nil, false
}
func (s *stubOdigosConfigExtension) IsActiveSource(pcommon.Resource) bool { return true }
func (s *stubOdigosConfigExtension) GetWorkloadCacheKey(pcommon.Resource) (string, error) {
	return s.key, s.err
}
func (s *stubOdigosConfigExtension) GetWorkloadIdentityFromResource(pcommon.Resource) (string, pcommon.Map, error) {
	if s.err != nil {
		return "", pcommon.NewMap(), s.err
	}
	return s.key, s.attrs, nil
}
func (s *stubOdigosConfigExtension) RegisterWorkloadConfigCacheCallback(collector.WorkloadConfigCacheCallback) {
}
func (s *stubOdigosConfigExtension) UnregisterWorkloadConfigCacheCallback(collector.WorkloadConfigCacheCallback) {
}
func (s *stubOdigosConfigExtension) WaitForCacheSync(context.Context) bool { return true }
func (s *stubOdigosConfigExtension) GetDataStreamsForWorkload(pcommon.Resource) ([]string, bool) {
	return nil, false
}

func startBatcher(t *testing.T, e *urlTemplatizationExporter, c pathCounter) {
	t.Helper()
	if e.cfg.FlushInterval <= 0 {
		e.batcher.flushInterval = time.Hour
	}
	e.batcher.start(c)
}

func stopBatcher(e *urlTemplatizationExporter) {
	_ = e.batcher.stop()
}

func waitRedis(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, time.Second, time.Millisecond, msg)
}

func TestConsumeTraces_CountsServerAndClientSeparately(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	extID := component.MustNewID("odigos_config_k8s")
	e := newExporter(zap.NewNop(), &Config{
		OdigosConfigExtension: &extID,
		Endpoint:              mr.Addr(),
		MaxPathsPerWorkload:   100,
		FlushThreshold:        1,
	})
	e.provider = &stubOdigosConfigExtension{key: "payments/Deployment/checkout/app"}
	startBatcher(t, e, &redisPathCounter{rdb: rdb, maxPaths: 100, pathIdleTTL: time.Hour, logger: zap.NewNop()})

	td := ptrace.NewTraces()
	spans := td.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()

	server := spans.AppendEmpty()
	server.SetKind(ptrace.SpanKindServer)
	server.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	server.Attributes().PutStr(semconv.AttributeURLPath, "/users/42")

	client := spans.AppendEmpty()
	client.SetKind(ptrace.SpanKindClient)
	client.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	client.Attributes().PutStr(semconv.AttributeURLPath, "/users/42")

	// framework-set route (no odigos result) — ignored
	withRoute := spans.AppendEmpty()
	withRoute.SetKind(ptrace.SpanKindServer)
	withRoute.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	withRoute.Attributes().PutStr(semconv.AttributeURLPath, "/users/42")
	withRoute.Attributes().PutStr(semconv.AttributeHTTPRoute, "/users/{id}")

	// custom_rule — ignored
	customRule := spans.AppendEmpty()
	customRule.SetKind(ptrace.SpanKindClient)
	customRule.Attributes().PutStr(deprecatedsemconv.AttributeHTTPMethod, "POST")
	customRule.Attributes().PutStr(deprecatedsemconv.AttributeHTTPTarget, "/orders/9?x=1")
	customRule.Attributes().PutStr(semconv.AttributeURLTemplate, "/orders/{id}")
	customRule.Attributes().PutStr(odigosattributes.UrlTemplatizationResultAttribute, string(odigosattributes.UrlTemplatizationResultCustomRule))

	// default_heuristic — still counted
	heuristic := spans.AppendEmpty()
	heuristic.SetKind(ptrace.SpanKindServer)
	heuristic.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	heuristic.Attributes().PutStr(semconv.AttributeURLPath, "/items/7")
	heuristic.Attributes().PutStr(semconv.AttributeHTTPRoute, "/items/{id}")
	heuristic.Attributes().PutStr(odigosattributes.UrlTemplatizationResultAttribute, string(odigosattributes.UrlTemplatizationResultDefaultHeuristic))

	internal := spans.AppendEmpty()
	internal.SetKind(ptrace.SpanKindInternal)
	internal.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	internal.Attributes().PutStr(semconv.AttributeURLPath, "/internal")

	require.NoError(t, e.consumeTraces(context.Background(), td))

	serverKey := "odigos:urltempl:unmatched:server:payments/Deployment/checkout/app"
	clientKey := "odigos:urltempl:unmatched:client:payments/Deployment/checkout/app"
	waitRedis(t, func() bool {
		return mr.HGet(serverKey, "/users/42") == "1" &&
			mr.HGet(serverKey, "/items/7") == "1" &&
			mr.HGet(clientKey, "/users/42") == "1"
	}, "counted unmatched server and client paths")
	stopBatcher(e)

	assert.Equal(t, "", mr.HGet(clientKey, "/orders/9"))
	assert.Equal(t, int64(2), rdb.HLen(context.Background(), serverKey).Val())
	assert.Equal(t, int64(1), rdb.HLen(context.Background(), clientKey).Val())
}

func TestConsumeTraces_SkipsNewPathsWhenHashFull(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	extID := component.MustNewID("odigos_config_k8s")
	e := newExporter(zap.NewNop(), &Config{
		OdigosConfigExtension: &extID,
		Endpoint:              mr.Addr(),
		MaxPathsPerWorkload:   2,
		FlushThreshold:        2,
	})
	e.provider = &stubOdigosConfigExtension{key: "ns/Deployment/svc/c"}
	startBatcher(t, e, &redisPathCounter{rdb: rdb, maxPaths: 2, pathIdleTTL: time.Hour, logger: zap.NewNop()})

	serverKey := "odigos:urltempl:unmatched:server:ns/Deployment/svc/c"
	require.NoError(t, rdb.HSet(context.Background(), serverKey, "/a", 5, "/b", 3).Err())

	td := ptrace.NewTraces()
	spans := td.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()

	// existing path — still increments
	existing := spans.AppendEmpty()
	existing.SetKind(ptrace.SpanKindServer)
	existing.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	existing.Attributes().PutStr(semconv.AttributeURLPath, "/a")

	// new path — skipped
	newbie := spans.AppendEmpty()
	newbie.SetKind(ptrace.SpanKindServer)
	newbie.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	newbie.Attributes().PutStr(semconv.AttributeURLPath, "/c")

	require.NoError(t, e.consumeTraces(context.Background(), td))
	waitRedis(t, func() bool {
		return mr.HGet(serverKey, "/a") == "6"
	}, "existing path still increments")
	stopBatcher(e)

	assert.Equal(t, "3", mr.HGet(serverKey, "/b"))
	assert.Equal(t, "", mr.HGet(serverKey, "/c"))
	assert.Equal(t, int64(2), rdb.HLen(context.Background(), serverKey).Val())
}

func TestConsumeTraces_ResolvesPathFromFullUrl(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	extID := component.MustNewID("odigos_config_k8s")
	e := newExporter(zap.NewNop(), &Config{
		OdigosConfigExtension: &extID,
		Endpoint:              mr.Addr(),
		MaxPathsPerWorkload:   100,
		FlushThreshold:        1,
	})
	e.provider = &stubOdigosConfigExtension{key: "default/Deployment/frontend/app"}
	startBatcher(t, e, &redisPathCounter{rdb: rdb, maxPaths: 100, pathIdleTTL: time.Hour, logger: zap.NewNop()})

	td := ptrace.NewTraces()
	spans := td.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()

	// client with only url.full (common for browser/Java HttpURLConnection client spans)
	client := spans.AppendEmpty()
	client.SetKind(ptrace.SpanKindClient)
	client.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	client.Attributes().PutStr(semconv.AttributeURLFull, "http://cart:7070/cart?sessionId=abc")
	// instrumentation may set http.route on client spans; processor only checks url.template
	client.Attributes().PutStr(semconv.AttributeHTTPRoute, "/cart")

	// legacy http.url fallback
	legacy := spans.AppendEmpty()
	legacy.SetKind(ptrace.SpanKindClient)
	legacy.Attributes().PutStr(deprecatedsemconv.AttributeHTTPMethod, "POST")
	legacy.Attributes().PutStr(deprecatedsemconv.AttributeHTTPURL, "https://checkout:8080/checkout/1")

	require.NoError(t, e.consumeTraces(context.Background(), td))

	clientKey := "odigos:urltempl:unmatched:client:default/Deployment/frontend/app"
	waitRedis(t, func() bool {
		return mr.HGet(clientKey, "/cart") == "1" && mr.HGet(clientKey, "/checkout/1") == "1"
	}, "resolved paths from url.full and http.url")
	stopBatcher(e)
}

func TestConsumeTraces_SkipsWhenIdentityLookupFails(t *testing.T) {
	extID := component.MustNewID("odigos_config_k8s")
	e := newExporter(zap.NewNop(), &Config{OdigosConfigExtension: &extID, Endpoint: "unused", MaxPathsPerWorkload: 10})
	e.provider = &stubOdigosConfigExtension{err: errors.New("no workload")}

	td := ptrace.NewTraces()
	span := td.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetKind(ptrace.SpanKindServer)
	span.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
	span.Attributes().PutStr(semconv.AttributeURLPath, "/users/42")

	require.NoError(t, e.consumeTraces(context.Background(), td))
}

func TestUnmatchedRedisKey(t *testing.T) {
	assert.Equal(t, "odigos:urltempl:unmatched:server:w", unmatchedRedisKey("w", ptrace.SpanKindServer))
	assert.Equal(t, "odigos:urltempl:unmatched:client:w", unmatchedRedisKey("w", ptrace.SpanKindClient))
	assert.Equal(t, "", unmatchedRedisKey("w", ptrace.SpanKindInternal))
}

func TestConfigValidate(t *testing.T) {
	assert.Error(t, (&Config{}).Validate())
	extID := component.MustNewID("odigos_config_k8s")
	assert.Error(t, (&Config{OdigosConfigExtension: &extID, MaxPathsPerWorkload: 10, FlushInterval: time.Second, FlushThreshold: 1}).Validate())
	assert.Error(t, (&Config{OdigosConfigExtension: &extID, Endpoint: "x:6379", MaxPathsPerWorkload: 0, FlushInterval: time.Second, FlushThreshold: 1, PathIdleTTL: time.Hour}).Validate())
	assert.Error(t, (&Config{OdigosConfigExtension: &extID, Endpoint: "x:6379", MaxPathsPerWorkload: 10, FlushInterval: 0, FlushThreshold: 1, PathIdleTTL: time.Hour}).Validate())
	assert.Error(t, (&Config{OdigosConfigExtension: &extID, Endpoint: "x:6379", MaxPathsPerWorkload: 10, FlushInterval: time.Second, FlushThreshold: 0, PathIdleTTL: time.Hour}).Validate())
	assert.Error(t, (&Config{OdigosConfigExtension: &extID, Endpoint: "x:6379", MaxPathsPerWorkload: 10, FlushInterval: time.Second, FlushThreshold: 1, PathIdleTTL: 0}).Validate())
	assert.NoError(t, (&Config{OdigosConfigExtension: &extID, Endpoint: "x:6379", MaxPathsPerWorkload: 10, FlushInterval: time.Second, FlushThreshold: 1, PathIdleTTL: time.Hour}).Validate())
}

func TestFactoryType(t *testing.T) {
	assert.Equal(t, "odigos_url_templatization", NewFactory().Type().String())
	cfg := createDefaultConfig().(*Config)
	assert.Equal(t, 0, cfg.MaxPathsPerWorkload)
	assert.Equal(t, time.Duration(0), cfg.PathIdleTTL)
	assert.Equal(t, defaultFlushInterval, cfg.FlushInterval)
	assert.Equal(t, defaultFlushThreshold, cfg.FlushThreshold)
}

func TestIncrPaths_LuaCapsAcrossKeysInOneEval(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	c := &redisPathCounter{rdb: rdb, maxPaths: 2, pathIdleTTL: time.Hour, logger: zap.NewNop()}
	serverKey := "odigos:urltempl:unmatched:server:ns/Deployment/svc/c"
	clientKey := "odigos:urltempl:unmatched:client:ns/Deployment/svc/c"
	require.NoError(t, rdb.HSet(context.Background(), serverKey, "/a", 1).Err())

	require.NoError(t, c.incrPaths(context.Background(), map[string]map[string]int64{
		serverKey: {
			"/a": 2, // existing — always increments
			"/b": 1, // fills the hash
			"/c": 1, // skipped
			"/d": 1, // skipped
		},
		clientKey: {
			"/x": 1,
			"/y": 1,
			"/z": 1, // skipped — client hash also capped at 2
		},
	}))

	assert.Equal(t, "3", mr.HGet(serverKey, "/a"))
	assert.Equal(t, int64(2), rdb.HLen(context.Background(), serverKey).Val())
	newServer := 0
	for _, p := range []string{"/b", "/c", "/d"} {
		if mr.HGet(serverKey, p) != "" {
			newServer++
		}
	}
	assert.Equal(t, 1, newServer, "server hash should accept one new path under the cap")

	assert.Equal(t, int64(2), rdb.HLen(context.Background(), clientKey).Val())
	newClient := 0
	for _, p := range []string{"/x", "/y", "/z"} {
		if mr.HGet(clientKey, p) != "" {
			newClient++
		}
	}
	assert.Equal(t, 2, newClient, "client hash should accept two new paths under the cap")
}

func TestConsumeTraces_BatchesAcrossCallsUntilFlush(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	extID := component.MustNewID("odigos_config_k8s")
	e := newExporter(zap.NewNop(), &Config{
		OdigosConfigExtension: &extID,
		Endpoint:              mr.Addr(),
		MaxPathsPerWorkload:   100,
		FlushInterval:         time.Hour,
		FlushThreshold:        2,
	})
	e.provider = &stubOdigosConfigExtension{key: "ns/Deployment/svc/c"}
	startBatcher(t, e, &redisPathCounter{rdb: rdb, maxPaths: 100, pathIdleTTL: time.Hour, logger: zap.NewNop()})

	td := func(path string) ptrace.Traces {
		out := ptrace.NewTraces()
		span := out.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		span.SetKind(ptrace.SpanKindServer)
		span.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
		span.Attributes().PutStr(semconv.AttributeURLPath, path)
		return out
	}

	require.NoError(t, e.consumeTraces(context.Background(), td("/users/1")))
	require.NoError(t, e.consumeTraces(context.Background(), td("/users/1")))

	serverKey := "odigos:urltempl:unmatched:server:ns/Deployment/svc/c"
	assert.Equal(t, "", mr.HGet(serverKey, "/users/1"), "below threshold must not write yet")

	require.NoError(t, e.consumeTraces(context.Background(), td("/orders/2")))
	require.Eventually(t, func() bool {
		return mr.HGet(serverKey, "/users/1") == "2" && mr.HGet(serverKey, "/orders/2") == "1"
	}, time.Second, 5*time.Millisecond, "same path counts must merge across batches")
	stopBatcher(e)
}

type blockingPathCounter struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *blockingPathCounter) incrPaths(context.Context, map[string]map[string]int64) error {
	c.calls.Add(1)
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	<-c.release
	return nil
}

func (c *blockingPathCounter) Close() error { return nil }

func TestFlush_DropsWhenBatcherChannelFull(t *testing.T) {
	c := &blockingPathCounter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	extID := component.MustNewID("odigos_config_k8s")
	e := newExporter(zap.NewNop(), &Config{
		OdigosConfigExtension: &extID,
		Endpoint:              "unused",
		MaxPathsPerWorkload:   100,
		FlushThreshold:        1,
	})
	e.provider = &stubOdigosConfigExtension{key: "ns/Deployment/svc/c"}
	startBatcher(t, e, c)

	td := func(path string) ptrace.Traces {
		out := ptrace.NewTraces()
		span := out.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		span.SetKind(ptrace.SpanKindServer)
		span.Attributes().PutStr(semconv.AttributeHTTPRequestMethod, "GET")
		span.Attributes().PutStr(semconv.AttributeURLPath, path)
		return out
	}

	require.NoError(t, e.consumeTraces(context.Background(), td("/a")))
	<-c.started

	for i := 0; i < defaultFlushThreshold+8; i++ {
		require.NoError(t, e.consumeTraces(context.Background(), td("/extra")))
	}
	assert.Equal(t, int32(1), c.calls.Load(), "batcher must not start another redis write while the first is in flight")

	close(c.release)
	stopBatcher(e)
}
