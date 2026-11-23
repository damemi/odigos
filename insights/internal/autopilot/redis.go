package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/odigos-io/odigos/common/consts"
	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	redisCallTreeKeyPrefix = "odigos:autopilot:call-tree:"
	// redisCallTreeKeyTTL refreshes on each successful merge so idle transaction
	// call trees eventually drop; structure-only (no counts).
	redisCallTreeKeyTTL = 7 * 24 * time.Hour
)

// Merges call-tree structure for one trace's service invocations into Redis
// Hashes, reading the trace's profile stacks once.
//
//	ARGV[1] = profile stacks key for the trace
//	ARGV[2] = TTL seconds (refreshed on every call tree key handled)
//	ARGV[3] = JSON array of invocations: {"k": call tree key prefix (the
//	          source id), "t": transaction id (always the last key segment),
//	          "s": [span id hexes of the invocation]}
//
// Each stack is merged into the call tree of the invocation its span belongs
// to: HSETNX node fields into k\x1f<sampleType>\x1f<t> (structure only, no
// counts). Stacks whose span belongs to no invocation are ignored. Returns
// the call tree keys that gained a node.
//
// Hash field = sha1hex(parent\0frameType\0name)
// Hash value = parent\0frameType\0name  (parent is "root" or node id)
var mergeCallTreesScript = redis.NewScript(`
local profileKey = ARGV[1]
local ttl = tonumber(ARGV[2])
local invocations = cjson.decode(ARGV[3])

-- Span hex -> the invocations it belongs to (normally exactly one).
local bySpan = {}
for _, inv in ipairs(invocations) do
  for _, spanHex in ipairs(inv.s) do
    local list = bySpan[spanHex]
    if not list then
      list = {}
      bySpan[spanHex] = list
    end
    table.insert(list, inv)
  end
end

local stacks = redis.call('HGETALL', profileKey)

-- No profile hash (or empty): nothing to correlate for this trace.
if #stacks == 0 then
  return {}
end

-- Unit separator (same as Go "\x1f"); Lua 5.1 has no \xHH escapes / continue.
local sep = string.char(31)
local nul = string.char(0)
local touched = {}
local changed = {}

local function mergeStack(field, blob)
  -- Profile fields are "<spanHex>:<stackDigest>"; reject unknown shapes.
  local colon = string.find(field, ':', 1, true)
  if not colon then
    return
  end

  -- Only stacks linked to an invocation's spans belong in its call tree.
  local invs = bySpan[string.sub(field, 1, colon - 1)]
  if not invs then
    return
  end

  local data = cjson.decode(blob)

  -- Sample type is a key segment (events/samples/…); empty st cannot form a key.
  local st = data.st or ''
  if st == '' then
    return
  end

  for _, inv in ipairs(invs) do
    local callTreeKey = inv.k .. sep .. st .. sep .. inv.t
    local parent = 'root'

    -- Frames are already filtered to named jvm/hotspot by the profiles exporter.
    for _, fr in ipairs(data.frames) do
      local payload = parent .. nul .. fr.ft .. nul .. fr.f
      local nid = redis.sha1hex(payload)

      -- Insert only if this node is new under the call tree key.
      if redis.call('HSETNX', callTreeKey, nid, payload) == 1 then
        changed[callTreeKey] = true
      end

      parent = nid
    end

    touched[callTreeKey] = true
  end
end

for i = 1, #stacks, 2 do
  mergeStack(stacks[i], stacks[i + 1])
end

-- Refresh TTL on every call tree key handled (new nodes or not).
if ttl and ttl > 0 then
  for callTreeKey in pairs(touched) do
    redis.call('EXPIRE', callTreeKey, ttl)
  end
end

local changedKeys = {}
for callTreeKey in pairs(changed) do
  table.insert(changedKeys, callTreeKey)
end

return changedKeys
`)

type stackBlobFrame struct {
	FunctionName string `json:"f"`
	FrameType    string `json:"ft"`
}

type stackBlob struct {
	SampleType string           `json:"st"`
	Frames     []stackBlobFrame `json:"frames"`
}

type tracesRedis struct {
	rdb    redis.Cmdable
	closer func() error
	logger *commonlogger.OdigosLogger
}

// newTracesRedis builds the cacheDb client without dialing. go-redis connects
// lazily and redials on every command, so an unreachable Redis at startup
// (cacheDb still rolling out) heals once it comes up instead of disabling
// autopilot for the life of the pod.
func newTracesRedis(endpoint string, logger *commonlogger.OdigosLogger) *tracesRedis {
	client := redis.NewClient(&redis.Options{
		Addr:                  endpoint,
		DB:                    consts.RedisDBAutopilotProfilesTransactionsCorrelation,
		DialTimeout:           2 * time.Second,
		ReadTimeout:           5 * time.Second,
		WriteTimeout:          2 * time.Second,
		ContextTimeoutEnabled: true,
	})
	return &tracesRedis{
		rdb:    client,
		closer: client.Close,
		logger: logger,
	}
}

func (r *tracesRedis) ping(ctx context.Context) error {
	if err := r.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}
	return nil
}

func (r *tracesRedis) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer()
}

// redisProfileKey is the profiles exporter's stacks hash for a hex trace id.
func redisProfileKey(traceID string) string {
	return consts.RedisAutopilotProfileKeyPrefix + traceID
}

// redisCallTreeSourcePrefix is the key prefix for a source, by its insights
// source id (repo.HashSourceID, as in the transactions table). Full keys are
// prefix\x1f<sampleType>\x1f<txID> so the transaction id is always last.
func redisCallTreeSourcePrefix(sourceID int64) string {
	return redisCallTreeKeyPrefix + strconv.FormatInt(sourceID, 10)
}

func redisCallTreeKey(sourceID int64, sampleType, txID string) string {
	return redisCallTreeSourcePrefix(sourceID) + "\x1f" + sampleType + "\x1f" + txID
}

// redisInvocation is one service invocation as mergeCallTreesScript reads it.
type redisInvocation struct {
	KeyPrefix     string   `json:"k"`
	TransactionID string   `json:"t"`
	SpanIDs       []string `json:"s"`
}

// mergeCallTrees asks Redis to expand the trace's matching profile stacks into
// structure-only call tree Hashes (one key per invocation and sample type), in
// one script call for all the trace's invocations. traceID and the span ids are
// hex, as the profiles exporter keys them. Invocations without a transaction
// id, a source identity or span ids are skipped. Returns the call tree keys
// that gained at least one node.
func (r *tracesRedis) mergeCallTrees(ctx context.Context, traceID string, invocations []ServiceInvocation) ([]string, error) {
	if r == nil || r.rdb == nil || traceID == "" {
		return nil, nil
	}
	payload := make([]redisInvocation, 0, len(invocations))
	for _, inv := range invocations {
		if inv.TransactionID == "" {
			continue
		}
		if len(inv.SourceIdentity) == 0 {
			continue
		}
		spanIDs := make([]string, 0, len(inv.SpanIDs))
		for _, sid := range inv.SpanIDs {
			if sid != "" {
				spanIDs = append(spanIDs, sid)
			}
		}
		if len(spanIDs) == 0 {
			continue
		}
		payload = append(payload, redisInvocation{KeyPrefix: redisCallTreeSourcePrefix(repo.HashSourceID(inv.SourceIdentity)), TransactionID: inv.TransactionID, SpanIDs: spanIDs})
	}
	if len(payload) == 0 {
		return nil, nil
	}
	invocationsJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode service invocations: %w", err)
	}

	changed, err := mergeCallTreesScript.Run(ctx, r.rdb, nil,
		redisProfileKey(traceID),
		int(redisCallTreeKeyTTL/time.Second),
		string(invocationsJSON),
	).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("redis merge call trees: %w", err)
	}
	return changed, nil
}

// redisCallTreeKeyMeta is the source + sample-type identity encoded in a call tree key.
type redisCallTreeKeyMeta struct {
	SourceID      int64
	SampleType    string
	TransactionID int64
}

// parseRedisCallTreeKey splits odigos:autopilot:call-tree:{sourceID}\x1f{sampleType}\x1f{txID}.
func parseRedisCallTreeKey(key string) (redisCallTreeKeyMeta, bool) {
	rest, ok := strings.CutPrefix(key, redisCallTreeKeyPrefix)
	if !ok || rest == "" {
		return redisCallTreeKeyMeta{}, false
	}
	parts := strings.Split(rest, "\x1f")
	if len(parts) != 3 {
		return redisCallTreeKeyMeta{}, false
	}
	for _, p := range parts {
		if p == "" {
			return redisCallTreeKeyMeta{}, false
		}
	}
	sourceID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return redisCallTreeKeyMeta{}, false
	}
	txID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return redisCallTreeKeyMeta{}, false
	}
	return redisCallTreeKeyMeta{
		SourceID:      sourceID,
		SampleType:    parts[1],
		TransactionID: txID,
	}, true
}

// parseSyncableCallTreeKey is parseRedisCallTreeKey limited to the sample
// types the store keeps.
func parseSyncableCallTreeKey(key string) (redisCallTreeKeyMeta, bool) {
	meta, ok := parseRedisCallTreeKey(key)
	if !ok || (meta.SampleType != "events" && meta.SampleType != "samples") {
		return redisCallTreeKeyMeta{}, false
	}
	return meta, true
}

// parseCallTreeHashValue splits parent\0frameType\0name from a Redis hash value.
func parseCallTreeHashValue(raw string) (parent, frameType, functionName string, ok bool) {
	parts := strings.SplitN(raw, "\x00", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	parent, frameType, functionName = parts[0], parts[1], parts[2]
	if parent == "" || functionName == "" || (frameType != "jvm" && frameType != "hotspot") {
		return "", "", "", false
	}
	return parent, frameType, functionName, true
}

// loadCallTreeStructureNodes reads one call tree hash into structure-only call tree nodes.
func (r *tracesRedis) loadCallTreeStructureNodes(ctx context.Context, key string, meta redisCallTreeKeyMeta) ([]repo.CallTreeNode, error) {
	if r == nil || r.rdb == nil || key == "" {
		return nil, nil
	}
	fields, err := r.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("hgetall %s: %w", key, err)
	}
	out := make([]repo.CallTreeNode, 0, len(fields))
	for nodeID, raw := range fields {
		if nodeID == "" {
			continue
		}
		parent, frameType, functionName, ok := parseCallTreeHashValue(raw)
		if !ok {
			if r.logger != nil {
				r.logger.Debug("skip malformed redis call tree node",
					"key", key,
					"node_id", nodeID,
				)
			}
			continue
		}
		out = append(out, repo.CallTreeNode{
			SourceID:      meta.SourceID,
			TransactionID: meta.TransactionID,
			NodeID:        nodeID,
			Parent:        parent,
			FunctionName:  functionName,
			FrameType:     frameType,
			SampleType:    meta.SampleType,
		})
	}
	return out, nil
}
