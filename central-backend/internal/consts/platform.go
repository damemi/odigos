package consts

type PlatformType string

const (
	PlatformTypeK8s PlatformType = "k8s"
	PlatformTypeVM  PlatformType = "vm"
	// PlatformTypeConnector is a cloud connector runtime. It registers over the
	// same reverse WebSocket as a VM agent (platformType=connector) and serves
	// the agents-api GraphQL schema, so Central routes to it like any other proxy.
	PlatformTypeConnector PlatformType = "connector"
)
