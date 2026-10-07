package odigosurltemplatizationexporter

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// incrUnmatchedPathsScript writes one flush batch of unmatched-path counts.
//
// Redis schema: each KEY is a HASH, one per (span kind × workload):
//
//	odigos:urltempl:unmatched:server:{ns}/{kind}/{name}/{container}
//	odigos:urltempl:unmatched:client:{ns}/{kind}/{name}/{container}
//
// Field name = URL path (e.g. /users/42). Value = integer hit count. Each field
// has its own sliding idle TTL (HEXPIRE); the hash key itself has no TTL.
// Server and client hashes are independent, each capped at maxPaths fields.
//
// A flush can touch many of those hashes. Doing HEXISTS + HINCRBY + HEXPIRE
// from Go would be one RTT per command (or a pipeline that still races on
// HLEN). This script runs the whole batch as a single EVAL: one network
// round-trip, and Redis executes it atomically so two collectors cannot both
// observe HLEN < maxPaths and both insert.
//
// For each field: increment if it already exists (and refresh HEXPIRE); insert
// only while HLEN < maxPaths; otherwise skip the new path. Idle fields expire
// on Redis; HLEN can lag until that happens.
//
// ARGV:
//
//	ARGV[1] = maxPaths
//	ARGV[2] = ttl seconds (sliding idle HEXPIRE; always positive)
//	then for each KEYS[i]: n, path_1, delta_1, ..., path_n, delta_n
//
// Returns how many new paths were skipped because their hash was full.
var incrUnmatchedPathsScript = redis.NewScript(`
local maxPaths = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local skipped = 0
-- Next unread ARGV index.
local argi = 3
for _, key in ipairs(KEYS) do
  local n = tonumber(ARGV[argi])
  argi = argi + 1
  local room = maxPaths - redis.call('HLEN', key)
  for _ = 1, n do
    local path = ARGV[argi]
    local delta = tonumber(ARGV[argi + 1])
    argi = argi + 2
    local isNew = redis.call('HEXISTS', key, path) == 0
    if isNew and room <= 0 then
      -- New path and the hash is full: drop it.
      skipped = skipped + 1
    else
      -- Existing path, or new path with room: count it and refresh TTL.
      if isNew then
        room = room - 1
      end
      redis.call('HINCRBY', key, path, delta)
      redis.call('HEXPIRE', key, ttl, 'FIELDS', 1, path)
    end
  end
end
return skipped
`)

// pathCounter increments per-path counts, capped per key.
type pathCounter interface {
	io.Closer
	// incrPaths applies deltas keyed by counter key -> path -> delta.
	// When a key is at maxPaths, new paths are skipped; existing paths still increment.
	incrPaths(ctx context.Context, increments map[string]map[string]int64) error
}

type redisPathCounter struct {
	rdb         redis.Cmdable
	closer      func() error
	maxPaths    int
	pathIdleTTL time.Duration
	logger      *zap.Logger
}

func newRedisPathCounter(endpoint string, maxPaths int, pathIdleTTL time.Duration, logger *zap.Logger) (*redisPathCounter, error) {
	client := redis.NewClient(&redis.Options{
		Addr:                  endpoint,
		DialTimeout:           2 * time.Second,
		ReadTimeout:           2 * time.Second,
		WriteTimeout:          2 * time.Second,
		ContextTimeoutEnabled: true,
	})
	return &redisPathCounter{
		rdb:         client,
		closer:      client.Close,
		maxPaths:    maxPaths,
		pathIdleTTL: pathIdleTTL,
		logger:      logger,
	}, nil
}

func (c *redisPathCounter) Close() error {
	if c.closer != nil {
		return c.closer()
	}
	return nil
}

func (c *redisPathCounter) incrPaths(ctx context.Context, increments map[string]map[string]int64) error {
	if len(increments) == 0 {
		return nil
	}

	ttlSeconds := int(c.pathIdleTTL / time.Second)

	keys := make([]string, 0, len(increments))
	// ARGV: maxPaths, ttl + per key (n + 2*n path/delta). Cap roughly.
	args := make([]any, 0, 2+3*len(increments))
	args = append(args, c.maxPaths, ttlSeconds)

	for key, paths := range increments {
		pairs := make([]any, 0, 2*len(paths))
		for path, delta := range paths {
			if delta == 0 {
				continue
			}
			pairs = append(pairs, path, delta)
		}
		if len(pairs) == 0 {
			continue
		}
		keys = append(keys, key)
		args = append(args, len(pairs)/2)
		args = append(args, pairs...)
	}
	if len(keys) == 0 {
		return nil
	}

	skipped, err := incrUnmatchedPathsScript.Run(ctx, c.rdb, keys, args...).Int64()
	if err != nil {
		return fmt.Errorf("failed to run redis increment script: %w", err)
	}
	if skipped > 0 {
		c.logger.Debug("skipped new unmatched paths: hash full",
			zap.Int("keys", len(keys)),
			zap.Int64("skipped", skipped),
			zap.Int("max_paths", c.maxPaths),
			zap.Duration("path_idle_ttl", c.pathIdleTTL),
		)
	}
	return nil
}
