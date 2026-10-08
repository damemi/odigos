package urltemplatization

import (
	"context"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/common/urltemplate"
	"github.com/redis/go-redis/v9"
)

// delEmptyHashScript deletes KEYS[1] only when the hash is empty.
// Atomic HLEN+DEL avoids racing with concurrent HINCRBY from the unmatched exporter.
var delEmptyHashScript = redis.NewScript(`
if redis.call("HLEN", KEYS[1]) == 0 then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// gcMatchedPaths deletes the fields of the unmatched-path hash key that already
// match an accepted templatization rule, then drops the hash atomically if it is
// left empty. Deleted paths are removed from fields so later steps see only the
// remaining unmatched paths.
func (j *LiveTrafficLearningJob) gcMatchedPaths(ctx context.Context, logger *commonlogger.OdigosLogger, key string, fields map[string]string, rules map[int][]urltemplate.PathRule) {
	matchedPaths := pathsMatchingRules(fields, rules)
	if len(matchedPaths) > 0 {
		if err := j.rdb.HDel(ctx, key, matchedPaths...).Err(); err != nil {
			logger.Error("failed to delete paths matching existing rules from redis",
				"key", key, "count", len(matchedPaths), "err", err)
		} else {
			logger.Debug("garbage-collected unmatched paths matching existing rules",
				"key", key, "count", len(matchedPaths))
		}
		for _, path := range matchedPaths {
			delete(fields, path)
		}
	}
	if len(fields) == 0 {
		// Only drop the key if Redis still reports an empty hash (atomic),
		// so concurrent unmatched-path increments are not wiped.
		if err := delEmptyHashScript.Run(ctx, j.rdb, []string{key}).Err(); err != nil {
			logger.Error("failed to delete empty unmatched path key from redis", "key", key, "err", err)
		}
	}
}

// pathsMatchingRules returns Redis hash field names that already match an
// accepted templatization rule. rules is keyed by segment count.
func pathsMatchingRules(pathCounts map[string]string, rules map[int][]urltemplate.PathRule) []string {
	if len(pathCounts) == 0 || len(rules) == 0 {
		return nil
	}
	matched := make([]string, 0)
	for path := range pathCounts {
		if urltemplate.PathMatchesAnyRule(path, rules) {
			matched = append(matched, path)
		}
	}
	return matched
}
