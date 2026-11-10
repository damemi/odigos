// Package types holds MuleSoft connector type name constants used by the provider runtime.
// Values match connectors/spec/data/mulesoft.yaml (workloads.*.types[].name).
package types

const (
	// CloudHub2 is a Mule application deployed to a CloudHub 2.0 shared space.
	CloudHub2 = "mulesoft.cloudhub2-app"
	// PrivateSpace is a Mule application deployed to a CloudHub 2.0 private space. It is discovered from the same
	// Application Manager scan as CloudHub2 and instrumented the same way, but kept a type of its own so the spec
	// can declare its instrumentation separately (unimplemented until it is verified on a real private space).
	PrivateSpace = "mulesoft.private-space-app"
	// RTF is a Mule application deployed to a Runtime Fabric.
	RTF = "mulesoft.rtf-app"
	// OmniGateway is an Omni Gateway (formerly Flex Gateway): the API gateway in front of Mule and non-Mule APIs.
	// It is instrumented by turning on its own OpenTelemetry tracing, which Odigos can only write on a Managed
	// Omni Gateway; a self-managed one keeps its configuration on the customer's host.
	OmniGateway = "mulesoft.omni-gateway"
	// Hybrid is a Mule application deployed through Runtime Manager to a customer-managed server.
	Hybrid = "mulesoft.hybrid-app"
)

// Language is what MuleSoft workloads report as their runtime language. Every Mule app runs the Mule runtime,
// and its Direct Telemetry Stream — not a language agent — is what instrumentation configures, so the Mule
// runtime version (not the JVM's) is the version reported beside it.
const Language = "mule"
