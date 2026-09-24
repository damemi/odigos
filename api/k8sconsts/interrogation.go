package k8sconsts

const (
	// OdigosInterrogationServiceName is the Deployment / ServiceAccount name for
	// the enterprise interrogation control-loop service.
	OdigosInterrogationServiceName = "odigos-interrogation"

	// OdigosInterrogationClickHouseDatabase is the ClickHouse database used by
	// interrogation exporters and the interrogation service (same bundled
	// ClickHouse server as insights; different database name).
	OdigosInterrogationClickHouseDatabase = "interrogation"
)
