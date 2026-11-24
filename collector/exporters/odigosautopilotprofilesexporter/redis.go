package odigosautopilotprofilesexporter

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.uber.org/zap"

	"github.com/odigos-io/odigos/common/consts"
)

const (
	redisMaxStacksPerTrace = 500
	// stackDigestBytes is the sha1 prefix length identifying a distinct stack in a field.
	stackDigestBytes = 8
)

// Stores immutable stack blobs under one Hash per TraceId.
//
//	Key:   odigos:autopilot:profile:<hex(trace)>
//	Field: <hex(span)>:<hex(sha1(value)[:8])>
//	Value: JSON {"st":"...","frames":[{"f":"...","ft":"..."},...]}
//
// HSETNX keeps values immutable; EXPIRE on the trace key refreshes whole-trace TTL
// (2× groupbytrace wait_duration). New fields are dropped once the hash reaches
// redisMaxStacksPerTrace entries. Existing fields are never overwritten.
// ARGV[1]=ttl, ARGV[2]=maxStacks, then field/blob pairs. Returns {added, skipped}.
var appendProfileStacksScript = redis.NewScript(`
local ttl = tonumber(ARGV[1])
local maxStacks = tonumber(ARGV[2])
local added = 0
local skipped = 0
local room = maxStacks - redis.call('HLEN', KEYS[1])
for i = 3, #ARGV, 2 do
  if redis.call('HEXISTS', KEYS[1], ARGV[i]) == 1 then
    -- already present: immutable no-op
  elseif room <= 0 then
    skipped = skipped + 1
  else
    redis.call('HSET', KEYS[1], ARGV[i], ARGV[i + 1])
    added = added + 1
    room = room - 1
  end
end
if added > 0 then
  redis.call('EXPIRE', KEYS[1], ttl)
end
return {added, skipped}
`)

type stackBlobFrame struct {
	FunctionName string `json:"f"`
	FrameType    string `json:"ft"`
}

type stackBlob struct {
	SampleType string           `json:"st"`
	Frames     []stackBlobFrame `json:"frames"`
}

type profileFrame struct {
	FunctionName string
	FrameType    string
}

// profileStack is one linked sample stack written to Redis (not trie edges).
type profileStack struct {
	TraceID    []byte
	SpanID     []byte
	SampleType string
	Frames     []profileFrame
}

type profilesRedis struct {
	rdb    redis.Cmdable
	closer func() error
	logger *zap.Logger
	// keyTTL is the whole-trace EXPIRE (2× groupbytrace wait_duration).
	keyTTL time.Duration
}

// newProfilesRedis builds the cacheDb client without dialing. go-redis
// connects lazily and redials on every command, so a Redis that is unreachable
// at startup (cacheDb still rolling out) is picked up on a later flush instead
// of disabling the exporter for the life of the pod.
func newProfilesRedis(endpoint string, keyTTL time.Duration, logger *zap.Logger) (*profilesRedis, error) {
	if keyTTL <= 0 {
		return nil, fmt.Errorf("redis key TTL must be positive")
	}
	client := redis.NewClient(&redis.Options{
		Addr:                  endpoint,
		DB:                    consts.RedisDBAutopilotProfilesTransactionsCorrelation,
		DialTimeout:           2 * time.Second,
		ReadTimeout:           2 * time.Second,
		WriteTimeout:          2 * time.Second,
		ContextTimeoutEnabled: true,
	})
	return &profilesRedis{
		rdb:    client,
		closer: client.Close,
		logger: logger,
		keyTTL: keyTTL,
	}, nil
}

func (r *profilesRedis) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer()
}

func profileStacksFrom(pd pprofile.Profiles) []profileStack {
	dic := pd.Dictionary()
	stringTable := dic.StringTable()
	var out []profileStack

	rps := pd.ResourceProfiles()
	for i := 0; i < rps.Len(); i++ {
		sps := rps.At(i).ScopeProfiles()
		for j := 0; j < sps.Len(); j++ {
			profiles := sps.At(j).Profiles()
			for k := 0; k < profiles.Len(); k++ {
				profile := profiles.At(k)
				sampleType := stringAt(stringTable, profile.SampleType().TypeStrindex())
				samples := profile.Samples()
				for s := 0; s < samples.Len(); s++ {
					sample := samples.At(s)
					traceID, spanID := resolveLinkIDs(dic, sample.LinkIndex())
					if len(traceID) == 0 || len(spanID) == 0 {
						continue
					}
					names, frameTypes := resolveStackFrames(dic, sample.StackIndex())
					names, frameTypes = rootFirstJavaFrames(names, frameTypes)
					if len(names) == 0 {
						continue
					}
					frames := make([]profileFrame, len(names))
					for fi := range names {
						frames[fi] = profileFrame{FunctionName: names[fi], FrameType: frameTypes[fi]}
					}
					out = append(out, profileStack{
						TraceID:    traceID,
						SpanID:     spanID,
						SampleType: sampleType,
						Frames:     frames,
					})
				}
			}
		}
	}
	return out
}

func (r *profilesRedis) insertStacks(ctx context.Context, stacks []profileStack) error {
	if r == nil || r.rdb == nil || len(stacks) == 0 {
		return nil
	}
	// Group by trace only for the Redis write (one Lua EVAL per key).
	byTrace := make(map[string][]fieldBlob, 8)
	for i := range stacks {
		s := &stacks[i]
		if len(s.TraceID) == 0 || len(s.SpanID) == 0 || len(s.Frames) == 0 {
			continue
		}
		blob, field, err := encodeStackField(s)
		if err != nil {
			return err
		}
		traceHex := hex.EncodeToString(s.TraceID)
		byTrace[traceHex] = append(byTrace[traceHex], fieldBlob{field: field, blob: blob})
	}
	return r.writeEncoded(ctx, byTrace)
}

// writeEncoded runs one Lua script per trace key with that trace's field/blob pairs.
func (r *profilesRedis) writeEncoded(ctx context.Context, byTrace map[string][]fieldBlob) error {
	if r == nil || r.rdb == nil || len(byTrace) == 0 {
		return nil
	}

	ttlSeconds := int(r.keyTTL / time.Second)
	var added, skipped, stackN int64
	for traceHex, pairs := range byTrace {
		if len(pairs) == 0 {
			continue
		}
		stackN += int64(len(pairs))
		args := make([]any, 0, 2+2*len(pairs))
		args = append(args, ttlSeconds, redisMaxStacksPerTrace)
		for _, p := range pairs {
			args = append(args, p.field, p.blob)
		}
		key := consts.RedisAutopilotProfileKeyPrefix + traceHex
		res, err := appendProfileStacksScript.Run(ctx, r.rdb, []string{key}, args...).Int64Slice()
		if err != nil {
			return fmt.Errorf("redis append profile stacks: %w", err)
		}
		if len(res) >= 2 {
			added += res[0]
			skipped += res[1]
		}
	}
	if skipped > 0 {
		r.logger.Debug("dropped profile stacks: trace hash full",
			zap.Int64("skipped", skipped),
			zap.Int("max_stacks_per_trace", redisMaxStacksPerTrace),
		)
	}
	r.logger.Debug("inserted profile stacks into redis",
		zap.Int64("stacks", stackN),
		zap.Int64("new_fields", added),
		zap.Int64("skipped", skipped),
		zap.Int("traces", len(byTrace)),
	)
	return nil
}

type fieldBlob struct {
	field string
	blob  string
}

func encodeStackField(s *profileStack) (blob string, field string, err error) {
	frames := make([]stackBlobFrame, len(s.Frames))
	for i := range s.Frames {
		frames[i] = stackBlobFrame{
			FunctionName: s.Frames[i].FunctionName,
			FrameType:    s.Frames[i].FrameType,
		}
	}
	raw, err := json.Marshal(stackBlob{
		SampleType: s.SampleType,
		Frames:     frames,
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal stack blob: %w", err)
	}
	// Field is span id + stack digest: each distinct stack of a span is stored once.
	digest := sha1.Sum(raw)
	field = hex.EncodeToString(s.SpanID) + ":" + hex.EncodeToString(digest[:stackDigestBytes])
	return string(raw), field, nil
}
