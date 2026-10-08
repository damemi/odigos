package urltemplatization

// Redis hashes holding unmatched HTTP paths per workload container.
// Written by the url templatization exporter and garbage-collected by the instrumentor.
// Full key: {prefix}{namespace}/{kind}/{name}/{container}
const (
	unmatchedPathsRedisKeyPrefix = "odigos:urltempl:unmatched:"

	UnmatchedPathsRedisKeyPrefixServer = unmatchedPathsRedisKeyPrefix + "server:"
	UnmatchedPathsRedisKeyPrefixClient = unmatchedPathsRedisKeyPrefix + "client:"

	// UnmatchedPathsRedisKeyPattern matches both server and client keys, for SCAN.
	UnmatchedPathsRedisKeyPattern = unmatchedPathsRedisKeyPrefix + "*"
)
