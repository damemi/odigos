package consts

// Logical databases in odigos-cache (Redis), one per feature, so features never share a keyspace.
const (
	// RedisDBURLTemplatizationLiveTrafficLearning holds unmatched URL path counts
	// written by the url templatization exporter and learned from by the instrumentor.
	RedisDBURLTemplatizationLiveTrafficLearning = 0

	// RedisDBAutopilotProfilesTransactionsCorrelation holds profile stacks per trace,
	// written by the autopilot profiles exporter for correlating transactions with profiles.
	RedisDBAutopilotProfilesTransactionsCorrelation = 1
)

// RedisAutopilotProfileKeyPrefix prefixes the per-trace profile stacks hash in
// RedisDBAutopilotProfilesTransactionsCorrelation: the autopilot profiles
// exporter writes <prefix><hex(traceId)>, and insights reads it.
const RedisAutopilotProfileKeyPrefix = "odigos:autopilot:profile:"
