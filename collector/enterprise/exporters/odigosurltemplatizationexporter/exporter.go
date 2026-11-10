package odigosurltemplatizationexporter

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	deprecatedsemconv "go.opentelemetry.io/collector/semconv/v1.18.0"
	semconv "go.opentelemetry.io/collector/semconv/v1.27.0"
	"go.uber.org/zap"

	"github.com/odigos-io/odigos/common/collector"
	"github.com/odigos-io/odigos/common/odigosattributes"
)

type urlTemplatizationExporter struct {
	logger *zap.Logger
	cfg    *Config

	mu       sync.RWMutex
	provider collector.OdigosConfigExtension
	batcher  *pathBatcher
}

func newExporter(logger *zap.Logger, cfg *Config) *urlTemplatizationExporter {
	return &urlTemplatizationExporter{
		logger:  logger,
		cfg:     cfg,
		batcher: newPathBatcher(cfg.FlushInterval, cfg.FlushThreshold, logger),
	}
}

func (e *urlTemplatizationExporter) Start(ctx context.Context, host component.Host) error {
	extID := e.cfg.OdigosConfigExtension
	ext, ok := host.GetExtensions()[*extID]
	if !ok {
		return fmt.Errorf("odigos config extension %q not found", extID.String())
	}
	odigosExt, ok := ext.(collector.OdigosConfigExtension)
	if !ok {
		return fmt.Errorf("extension %q is not an OdigosConfigExtension (got %T)", extID.String(), ext)
	}

	counter, err := newRedisPathCounter(e.cfg.Endpoint, e.cfg.MaxPathsPerWorkload, e.cfg.PathIdleTTL, e.logger)
	if err != nil {
		return fmt.Errorf("create redis path counter: %w", err)
	}

	e.mu.Lock()
	e.provider = odigosExt
	e.mu.Unlock()
	e.batcher.start(counter)
	return nil
}

func (e *urlTemplatizationExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.provider = nil
	e.mu.Unlock()
	return e.batcher.stop()
}

// consumeTraces records unmatched HTTP server/client paths on the batcher.
// Runs on the gateway root traces pipeline after odigosurltemplate when present.
func (e *urlTemplatizationExporter) consumeTraces(ctx context.Context, td ptrace.Traces) error {
	e.mu.RLock()
	provider := e.provider
	e.mu.RUnlock()
	if provider == nil {
		return nil
	}

	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		workloadKey, _, err := provider.GetWorkloadIdentityFromResource(rs.Resource())
		if err != nil {
			e.logger.Debug("skip resource: GetWorkloadIdentityFromResource failed", zap.Error(err))
			continue
		}
		scopes := rs.ScopeSpans()
		for j := 0; j < scopes.Len(); j++ {
			spans := scopes.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				path, ok := unmatchedPath(span)
				if !ok {
					continue
				}
				e.batcher.recordPath(workloadKey, span.Kind(), path)
				e.logger.Debug("url templatization unmatched path",
					zap.String("workload", workloadKey),
					zap.String("span.kind", span.Kind().String()),
					zap.String("url.path", path),
				)
			}
		}
	}

	return nil
}

// unmatchedPath returns the URL path for an unmatched HTTP server/client span.
// ok is false when the span should not be counted.
// Attribute extraction matches odigosurltemplateprocessor: server uses http.route,
// client uses url.template; path from url.path/http.target/url.full/http.url.
// Spans already templated by a custom rule (or by the framework with no
// odigos.url_templatization.result) are skipped. Heuristic / normalization /
// static_path results are still counted so learning can observe those paths.
func unmatchedPath(span ptrace.Span) (path string, ok bool) {
	kind := span.Kind()
	attr := span.Attributes()
	if _, hasMethod := getHttpMethod(attr); !hasMethod {
		return "", false
	}

	// Same target attribute the processor would write / skip when already set.
	var targetAttr string
	switch kind {
	case ptrace.SpanKindClient:
		targetAttr = semconv.AttributeURLTemplate
	case ptrace.SpanKindServer:
		targetAttr = semconv.AttributeHTTPRoute
	default:
		return "", false
	}
	if _, found := attr.Get(targetAttr); found {
		result, hasResult := attr.Get(odigosattributes.UrlTemplatizationResultAttribute)
		if !hasResult || result.AsString() == string(odigosattributes.UrlTemplatizationResultCustomRule) {
			return "", false
		}
	}

	path, hasPath := resolveUrlPath(attr)
	if !hasPath || path == "" {
		return "", false
	}
	return path, true
}

// getHttpMethod matches odigosurltemplateprocessor.getHttpMethod.
func getHttpMethod(attr pcommon.Map) (string, bool) {
	if method, found := attr.Get(semconv.AttributeHTTPRequestMethod); found {
		return method.AsString(), true
	}
	if method, found := attr.Get(deprecatedsemconv.AttributeHTTPMethod); found {
		return method.AsString(), true
	}
	return "", false
}

func getUrlPath(attr pcommon.Map) (string, bool) {
	if urlPath, found := attr.Get(semconv.AttributeURLPath); found {
		return urlPath.AsString(), true
	}
	if httpTarget, found := attr.Get(deprecatedsemconv.AttributeHTTPTarget); found {
		path := strings.SplitN(httpTarget.AsString(), "?", 2)[0]
		return path, true
	}
	return "", false
}

func getFullUrl(attr pcommon.Map) (string, bool) {
	if fullUrl, found := attr.Get(semconv.AttributeURLFull); found {
		return fullUrl.AsString(), true
	}
	if httpUrl, found := attr.Get(deprecatedsemconv.AttributeHTTPURL); found {
		return httpUrl.AsString(), true
	}
	return "", false
}

// resolveUrlPath matches odigosurltemplateprocessor.resolveUrlPath.
func resolveUrlPath(attr pcommon.Map) (string, bool) {
	urlPath, urlPathFound := getUrlPath(attr)
	if urlPathFound {
		return urlPath, true
	}
	fullUrl, fullUrlFound := getFullUrl(attr)
	if fullUrlFound {
		parsed, err := url.Parse(fullUrl)
		if err != nil {
			return "", false
		}
		return parsed.Path, true
	}
	return "", false
}
