package sdks

import (
	"errors"

	"github.com/odigos-io/odigos/instrumentation"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
)

var errNoOTLPConn = errors.New("OTLP connection is nil")

func identifyingAttributeSet(attrs ...attribute.KeyValue) []attribute.KeyValue {
	identifyingAttrs := []attribute.KeyValue{}
	for _, attr := range attrs {
		switch attr.Key {
		case semconv.K8SDeploymentNameKey,
			semconv.K8SStatefulSetNameKey,
			semconv.K8SDaemonSetNameKey,
			semconv.K8SCronJobNameKey,
			semconv.K8SJobNameKey,

			semconv.K8SNamespaceNameKey,

			semconv.K8SContainerNameKey:
			identifyingAttrs = append(identifyingAttrs, attr)
		}
	}
	return identifyingAttrs
}

func podNameFromSettings(settings instrumentation.Settings) string {
	// getting the pod name from resource attributes is not the most elegant way of doing this
	// consider the instrumentation.Settings is declared in OSS, and we don't want to put too much details there
	for _, kv := range settings.ResourceAttributes {
		if kv.Key == semconv.K8SPodNameKey {
			return kv.Value.AsString()
		}
	}

	return ""
}
