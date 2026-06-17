package sdks

import (
	"errors"

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