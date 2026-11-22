package autopilot

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

const (
	redisProfileKeyPrefix  = "odigos:autopilot:profile:"
	redisCallTreeKeyPrefix = "odigos:autopilot:call-tree:"
	// redisDB matches the profiles exporter (DB 1; URL-templatization uses DB 0).
	redisDB = 1
	// redisCallTreeKeyTTL refreshes on each successful merge so idle transaction
	// call trees eventually drop; structure-only (no counts).
	redisCallTreeKeyTTL = 7 * 24 * time.Hour
)

// Merges call-tree structure for one transaction into Redis Hashes.
//
//	ARGV[1] = call tree key prefix (workload only: ns/kind/name/container)
//	ARGV[2] = transaction id (always the last key segment)
//	ARGV[3] = profile stacks key for the TraceId
//	ARGV[4] = TTL seconds (refresh when nodes are added)
//	ARGV[5..] = span-id hexes belonging to the transaction
//
// Reads profile stacks for the trace, keeps only stacks whose span matches,
// and HSETNX node fields into prefix\x1f<sampleType>\x1f<txID> (structure only,
// no counts). Returns newly inserted nodes.
//
// Hash field = sha1hex(parent\0frameType\0name)
// Hash value = parent\0frameType\0name  (parent is "root" or node id)
var mergeCallTreeScript = redis.NewScript(`
local keyPrefix = ARGV[1]
local txID = ARGV[2]
local profileKey = ARGV[3]
local ttl = tonumber(ARGV[4])

local spanSet = {}
for i = 5, #ARGV do
  spanSet[ARGV[i]] = true
end

local stacks = redis.call('HGETALL', profileKey)

-- No profile hash (or empty): nothing to correlate for this TraceId.
if #stacks == 0 then
  return 0
end

-- Unit separator (same as Go "\x1f"); Lua 5.1 has no \xHH escapes / continue.
local sep = string.char(31)
local nul = string.char(0)
local added = 0

local function mergeStack(field, blob)
  -- Profile fields are "<spanHex>:<stackDigest>"; reject unknown shapes.
  local colon = string.find(field, ':', 1, true)
  if not colon then
    return
  end

  local spanHex = string.sub(field, 1, colon - 1)

  -- Only stacks linked to spans in this transaction belong in its call tree.
  if not spanSet[spanHex] then
    return
  end

  local data = cjson.decode(blob)

  -- Sample type is a key segment (events/samples/…); empty st cannot form a key.
  local st = data.st or ''
  if st == '' then
    return
  end

  local callTreeKey = keyPrefix .. sep .. st .. sep .. txID
  local parent = 'root'

  -- Frames are already filtered to named jvm/hotspot by the profiles exporter.
  for _, fr in ipairs(data.frames) do
    local payload = parent .. nul .. fr.ft .. nul .. fr.f
    local nid = redis.sha1hex(payload)

    -- Insert only if this node is new under the call tree key.
    if redis.call('HSETNX', callTreeKey, nid, payload) == 1 then
      added = added + 1
    end

    parent = nid
  end

  -- Refresh TTL whenever this call tree key is handled (new nodes or not).
  if ttl and ttl > 0 then
    redis.call('EXPIRE', callTreeKey, ttl)
  end
end

for i = 1, #stacks, 2 do
  mergeStack(stacks[i], stacks[i + 1])
end

return added
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
		DB:                    redisDB,
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

func redisProfileKey(traceID []byte) string {
	return redisProfileKeyPrefix + hex.EncodeToString(traceID)
}

// redisCallTreeWorkloadPrefix is the key prefix for a workload container.
// Full keys are prefix\x1f<sampleType>\x1f<txID> so the transaction id is always last.
func redisCallTreeWorkloadPrefix(namespace, kind, name, container string) string {
	return redisCallTreeKeyPrefix +
		namespace + "\x1f" +
		kind + "\x1f" +
		name + "\x1f" +
		container
}

func redisCallTreeKey(namespace, kind, name, container, sampleType, txID string) string {
	return redisCallTreeWorkloadPrefix(namespace, kind, name, container) + "\x1f" + sampleType + "\x1f" + txID
}

// mergeCallTree asks Redis to expand matching profile stacks into
// structure-only call tree Hashes (one key per sample type). Returns newly inserted nodes.
func (r *tracesRedis) mergeCallTree(ctx context.Context, tx TraceTransaction, traceID []byte) (int64, error) {
	if r == nil || r.rdb == nil || len(traceID) == 0 || tx.TransactionID == "" {
		return 0, nil
	}
	spanHexes := make([]any, 0, len(tx.SpanIDs))
	for _, sid := range tx.SpanIDs {
		if sid == "" {
			continue
		}
		spanHexes = append(spanHexes, hex.EncodeToString([]byte(sid)))
	}
	if len(spanHexes) == 0 {
		return 0, nil
	}

	args := make([]any, 0, 4+len(spanHexes))
	args = append(args,
		redisCallTreeWorkloadPrefix(tx.Namespace, tx.WorkloadKind, tx.WorkloadName, tx.ContainerName),
		tx.TransactionID,
		redisProfileKey(traceID),
		int(redisCallTreeKeyTTL/time.Second),
	)
	args = append(args, spanHexes...)

	n, err := mergeCallTreeScript.Run(ctx, r.rdb, nil, args...).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis merge call tree: %w", err)
	}
	return n, nil
}

// redisCallTreeKeyMeta is the workload + sample-type identity encoded in a call tree key.
type redisCallTreeKeyMeta struct {
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
	SampleType    string
	TransactionID string
}

// parseRedisCallTreeKey splits odigos:autopilot:call-tree:{ns}\x1f{kind}\x1f{name}\x1f{container}\x1f{sampleType}\x1f{txID}.
func parseRedisCallTreeKey(key string) (redisCallTreeKeyMeta, bool) {
	rest, ok := strings.CutPrefix(key, redisCallTreeKeyPrefix)
	if !ok || rest == "" {
		return redisCallTreeKeyMeta{}, false
	}
	parts := strings.Split(rest, "\x1f")
	if len(parts) != 6 {
		return redisCallTreeKeyMeta{}, false
	}
	for _, p := range parts {
		if p == "" {
			return redisCallTreeKeyMeta{}, false
		}
	}
	return redisCallTreeKeyMeta{
		Namespace:     parts[0],
		WorkloadKind:  parts[1],
		WorkloadName:  parts[2],
		ContainerName: parts[3],
		SampleType:    parts[4],
		TransactionID: parts[5],
	}, true
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

// scanCallTreeKeys lists all Redis call tree hash keys via SCAN.
func (r *tracesRedis) scanCallTreeKeys(ctx context.Context) ([]string, error) {
	if r == nil || r.rdb == nil {
		return nil, nil
	}
	var (
		cursor uint64
		out    []string
	)
	match := redisCallTreeKeyPrefix + "*"
	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, match, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan call tree keys: %w", err)
		}
		out = append(out, keys...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return out, nil
}

// loadCallTreeStructureNodes reads one call tree hash into structure-only call tree nodes.
func (r *tracesRedis) loadCallTreeStructureNodes(ctx context.Context, key string, meta redisCallTreeKeyMeta) ([]store.FlushedCallTreeNode, error) {
	if r == nil || r.rdb == nil || key == "" {
		return nil, nil
	}
	fields, err := r.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("hgetall %s: %w", key, err)
	}
	out := make([]store.FlushedCallTreeNode, 0, len(fields))
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
		out = append(out, store.FlushedCallTreeNode{
			Namespace:     meta.Namespace,
			WorkloadKind:  meta.WorkloadKind,
			WorkloadName:  meta.WorkloadName,
			ContainerName: meta.ContainerName,
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

func (r *tracesRedis) deleteKey(ctx context.Context, key string) error {
	if r == nil || r.rdb == nil || key == "" {
		return nil
	}
	if err := r.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("del %s: %w", key, err)
	}
	return nil
}
