// Package semconv is the single place the engine resolves OpenTelemetry
// semantic-convention attribute names, targeting semconv v1.27.0. Typed
// attribute.Key constants are bound to plain string vars so the rest of the
// engine can index map[string]string directly without repeating string casts.
// Attributes deprecated in v1.27 but still emitted by older instrumentations we
// ingest live in the "legacy fallback" block below.
package semconv

import (
	"strconv"
	"strings"

	otelsemconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// Attribute keys read in scorers / transaction identification.
var (
	ServiceName      = string(otelsemconv.ServiceNameKey)
	K8SNamespaceName = string(otelsemconv.K8SNamespaceNameKey)
	K8SPodName       = string(otelsemconv.K8SPodNameKey)
	K8SContainerName = string(otelsemconv.K8SContainerNameKey)
	ServerAddress    = string(otelsemconv.ServerAddressKey)
	ServerPort       = string(otelsemconv.ServerPortKey)
	RPCSystem        = string(otelsemconv.RPCSystemKey)
	FaaSTrigger      = string(otelsemconv.FaaSTriggerKey)

	// Database attributes (D6). db.operation.name is only set when the
	// gateway's odigossqldboperationprocessor ran; the D6 scorer derives it
	// from db.query.text when absent.
	DBSystem         = string(otelsemconv.DBSystemKey)
	DBNamespace      = string(otelsemconv.DBNamespaceKey)
	DBOperationName  = string(otelsemconv.DBOperationNameKey)
	DBQueryText      = string(otelsemconv.DBQueryTextKey)
	DBCollectionName = string(otelsemconv.DBCollectionNameKey)

	// HTTP payload sizes (D7). Opt-in semconv attributes; body content is an
	// Odigos payload-collection fallback when size attrs are absent.
	HTTPRequestBodySize  = string(otelsemconv.HTTPRequestBodySizeKey)
	HTTPResponseBodySize = string(otelsemconv.HTTPResponseBodySizeKey)
	HTTPRequestBody      = "http.request.body"
	HTTPResponseBody     = "http.response.body"

	// Status codes used as optional transaction-identity dimensions (entry span).
	HTTPResponseStatusCode = string(otelsemconv.HTTPResponseStatusCodeKey)
	RPCGRPCStatusCode      = string(otelsemconv.RPCGRPCStatusCodeKey)

	// Request identity: plumbing the relation miner ignores, or id carriers
	// (a path embeds the id it addresses).
	HTTPRequestMethod = string(otelsemconv.HTTPRequestMethodKey)
	HTTPRoute         = string(otelsemconv.HTTPRouteKey)
	URLFull           = string(otelsemconv.URLFullKey)
	URLPath           = string(otelsemconv.URLPathKey)
	URLScheme         = string(otelsemconv.URLSchemeKey)
	RPCService        = string(otelsemconv.RPCServiceKey)
	RPCMethod         = string(otelsemconv.RPCMethodKey)
	PeerService       = string(otelsemconv.PeerServiceKey)

	// Messaging. Key, offset and partition are copied by the broker between a
	// producer and its consumers: the transport-mirrored fields.
	MessagingSystem                 = string(otelsemconv.MessagingSystemKey)
	MessagingOperationType          = string(otelsemconv.MessagingOperationTypeKey)
	MessagingKafkaMessageKey        = string(otelsemconv.MessagingKafkaMessageKeyKey)
	MessagingKafkaOffset            = string(otelsemconv.MessagingKafkaOffsetKey)
	MessagingDestinationPartitionID = string(otelsemconv.MessagingDestinationPartitionIDKey)
	MessagingMessageID              = string(otelsemconv.MessagingMessageIDKey)
	MessagingMessageConversationID  = string(otelsemconv.MessagingMessageConversationIDKey)
)

// Odigos custom-instrumentation capture (D4). A custom-instrumented method
// produces a span carrying the probed code site and its captured values. The
// Java eBPF agent records up to five positional arguments as "arg.0".."arg.4"
// and the return value as "return.value" (both stringified via
// Object.toString() and truncated by the producer). These are Odigos-specific
// (not OTel semconv), so they live here as plain strings.
const (
	// CodeFunctionName is the current OTel semconv site attribute: a single,
	// fully-qualified name (e.g. "com.acme.UserService.getUser"). It supersedes
	// the CodeNamespace + CodeFunction pair, which the spec deprecated by folding
	// the namespace into this one value. The Java eBPF agent still emits the
	// deprecated pair today; D4 prefers this key when present and falls back to
	// the pair, so the derived site is identical across the migration. See
	// https://opentelemetry.io/docs/specs/semconv/registry/attributes/code/.
	CodeFunctionName = "code.function.name"

	// CodeNamespace / CodeFunction identify the probed site: the class/package
	// (e.g. "com.acme.UserService") and the method name (e.g. "getUser").
	// Deprecated by OTel in favor of CodeFunctionName (kept: current producers
	// still emit them).
	CodeNamespace = "code.namespace"
	CodeFunction  = "code.function"

	// ReturnValue is the stringified method return value.
	ReturnValue = "return.value"

	// Prefix of a captured positional argument ("arg.0").
	ArgPrefix = "arg."

	// MaxCapturedArgs mirrors the producer's positional-argument cap.
	MaxCapturedArgs = 5
)

// Captured positional argument i ("arg.0").
func ArgKey(i int) string { return ArgPrefix + strconv.Itoa(i) }

// Dotless spelling ("arg0") some extensions emit.
func LegacyArgKey(i int) string { return "arg" + strconv.Itoa(i) }

// A captured positional argument in either spelling.
func IsCapturedArg(key string) bool {
	if strings.HasPrefix(key, ArgPrefix) {
		return isDigits(key[len(ArgPrefix):])
	}
	return strings.HasPrefix(key, "arg") && isDigits(key[3:])
}

// Captured by custom instrumentation: an argument or the return value.
func IsProbeAttr(key string) bool {
	return key == ReturnValue || IsCapturedArg(key)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Legacy fallbacks - attributes we still consult because pre-v1.21
// instrumentations emit them. semconv no longer exposes typed constants for
// them, so they live here as plain strings with a TODO.
//
// TODO(insights-engine): drop once we drop support for pre-v1.21 OTel
// instrumentations on the producer side.
const (
	LegacyNetPeerName        = "net.peer.name"
	LegacyNetPeerPort        = "net.peer.port"
	LegacyHTTPHost           = "http.host"
	LegacyDBConnectionString = "db.connection_string"

	// DB fallbacks. db.statement and db.name are pre-v1.27 names; db.system.name
	// is the newer alias for db.system not yet exposed as a typed const in v1.27.
	LegacyDBStatement  = "db.statement"
	LegacyDBName       = "db.name"
	LegacyDBSystemName = "db.system.name"

	// HTTP body-size fallbacks for pre-v1.27 instrumentations.
	LegacyHTTPRequestContentLengthUncompressed  = "http.request_content_length_uncompressed"
	LegacyHTTPResponseContentLengthUncompressed = "http.response_content_length_uncompressed"
	LegacyHTTPRequestContentLength              = "http.request_content_length"
	LegacyHTTPResponseContentLength             = "http.response_content_length"

	// Legacy HTTP status (pre http.response.status_code).
	LegacyHTTPStatusCode = "http.status_code"

	// Pre-v1.23 HTTP request identity.
	LegacyHTTPMethod = "http.method"
	LegacyHTTPScheme = "http.scheme"
	LegacyHTTPFlavor = "http.flavor"

	// Pre-v1.25 db.operation and pre-v1.26 messaging.operation.
	LegacyDBOperation        = "db.operation"
	LegacyMessagingOperation = "messaging.operation"

	// Pre-v1.27 Kafka spellings of the offset and partition.
	LegacyMessagingKafkaMessageOffset = "messaging.kafka.message.offset"
	LegacyMessagingKafkaPartition     = "messaging.kafka.destination.partition"

	// Odigos header-capture naming (plural "headers"); distinct from OTel's
	// http.response.header.content-length.
	LegacyHTTPRequestHeadersContentLength  = "http.request.headers.content-length"
	LegacyHTTPResponseHeadersContentLength = "http.response.headers.content-length"
	LegacyHTTPRequestHeaderContentLength   = "http.request.header.content-length"
	LegacyHTTPResponseHeaderContentLength  = "http.response.header.content-length"
)

// Well-known attribute *values* we match against (semconv v1.27 exposes these
// as pre-built KeyValue attributes; we unwrap to the bare string since we
// compare against map[string]string entries).
var (
	RPCSystemGRPC    = otelsemconv.RPCSystemGRPC.Value.AsString()    // "grpc"
	FaaSTriggerTimer = otelsemconv.FaaSTriggerTimer.Value.AsString() // "timer"
)
