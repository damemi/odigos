package status

const (
	WorkloadOdigosHealthStatus = "WorkloadOdigosHealth"
)

type WorkloadOdigosHealthStatusReason string

const (
	WorkloadOdigosHealthStatusReasonCollectingTelemetry WorkloadOdigosHealthStatusReason = "CollectingTelemetry"
	WorkloadOdigosHealthStatusReasonDisabled            WorkloadOdigosHealthStatusReason = "DisabledForInstrumentation"
)
