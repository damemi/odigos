package k8sconsts

import "fmt"

const (
	// OdigosInterrogationRedisServiceName is the ClusterIP Service for the
	// bundled Redis deployed when interrogation is enabled.
	OdigosInterrogationRedisServiceName = "odigos-interrogation-redis"
	OdigosInterrogationRedisPort        = 6379
)

// InterrogationRedisEndpoint returns host:port for the interrogation Redis
// Service in the given namespace (Kubernetes cluster DNS).
func InterrogationRedisEndpoint(namespace string) string {
	return fmt.Sprintf("%s.%s:%d", OdigosInterrogationRedisServiceName, namespace, OdigosInterrogationRedisPort)
}
