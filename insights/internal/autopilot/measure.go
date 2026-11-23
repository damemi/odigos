package autopilot

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

const (
	redisMeasureKeyPrefix = "odigos:autopilot:measure:"
	redisMeasureKeyTTL    = 15 * time.Minute
	// maxMeasureCells caps the (transaction, site) cells kept per workload and
	// bucket; later ones are dropped. A run covers 4 transactions of up to 20
	// probes.
	maxMeasureCells = 256
	// maxMeasureWindow caps how far back probeTimings reads.
	maxMeasureWindow = 10 * time.Minute
)

// mergeMeasureScript merges one bucket's cells into the hash KEYS[1].
//
//	ARGV[1] = TTL seconds, ARGV[2] = message slots per cell
//	ARGV[3..] = op, field, number, text (4 per entry)
//
// op "i" adds the number to field, "x" keeps the larger value, and "e" counts
// the message in text under the cell prefix in field: the first writer owns a
// slot and later messages add to it, so replicas merge in any order.
var mergeMeasureScript = redis.NewScript(`
local slots = tonumber(ARGV[2])
for i = 3, #ARGV, 4 do
  local op, field, n = ARGV[i], ARGV[i+1], tonumber(ARGV[i+2])
  if op == 'i' then
    redis.call('HINCRBY', KEYS[1], field, n)
  elseif op == 'x' then
    local cur = tonumber(redis.call('HGET', KEYS[1], field))
    if not cur or n > cur then
      redis.call('HSET', KEYS[1], field, n)
    end
  else
    for slot = 0, slots - 1 do
      local cur = redis.call('HGET', KEYS[1], field .. 'm' .. slot)
      if not cur then
        redis.call('HSET', KEYS[1], field .. 'm' .. slot, ARGV[i+3])
        cur = ARGV[i+3]
      end
      if cur == ARGV[i+3] then
        redis.call('HINCRBY', KEYS[1], field .. 'c' .. slot, n)
        break
      end
    end
  end
end
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[1]))
return 0
`)

type measureBucket struct {
	namespace, kind, name string
	bucket                int64
}

type measureCell struct{ txID, site string }

// measureSet aggregates samples per workload bucket until the next flush.
type measureSet map[measureBucket]map[measureCell]*measure.Measure

// add counts an invocation's samples under its workload, which is how the
// leader reads them back: one interrogation covers every container.
func (s measureSet) add(inv ServiceInvocation) {
	if len(inv.Measures) == 0 {
		return
	}
	namespace := inv.SourceIdentity[semconv.K8SNamespaceName]
	kind, name := sourceidentity.K8sWorkloadKindAndNameFromResourceAttributesMap(inv.SourceIdentity)
	for _, sm := range inv.Measures {
		// A span with no start time has no bucket to count in.
		if sm.Bucket == 0 {
			continue
		}
		bk := measureBucket{namespace, kind, name, sm.Bucket}
		cells := s[bk]
		if cells == nil {
			cells = map[measureCell]*measure.Measure{}
			s[bk] = cells
		}
		c := measureCell{inv.TransactionID, sm.Site}
		m := cells[c]
		if m == nil {
			if len(cells) >= maxMeasureCells {
				continue
			}
			m = &measure.Measure{}
			cells[c] = m
		}
		sm.Msg = measure.CleanMessage(sm.Msg)
		m.Add(sm)
	}
}

func redisMeasureKey(bk measureBucket) string {
	return redisMeasureKeyPrefix + bk.namespace + "\x1f" + bk.kind + "\x1f" + bk.name + "\x1f" + strconv.FormatInt(bk.bucket, 10)
}

// mergeMeasures adds the aggregated cells to Redis, one script call per
// bucket. A failed bucket does not hold back the others.
func (r *tracesRedis) mergeMeasures(ctx context.Context, set measureSet) error {
	var firstErr error
	for bk, cells := range set {
		args := []any{int(redisMeasureKeyTTL / time.Second), measure.MaxErrors}
		for c, m := range cells {
			prefix := c.txID + "\x1f" + c.site + "\x1f"
			add := func(op, metric string, n int64) {
				args = append(args, op, prefix+metric, n, "")
			}
			add("i", "n", m.N)
			add("i", "err", m.Err)
			add("i", "sum", m.SumUs)
			add("x", "max", m.MaxUs)
			add("i", "self", m.SelfUs)
			if m.Spans > 0 {
				add("i", "spans", m.Spans)
			}
			for k, n := range m.Hist.Buckets {
				if n > 0 {
					add("i", "h"+strconv.Itoa(k), int64(n))
				}
			}
			for _, e := range m.Errors {
				args = append(args, "e", prefix, e.Count, e.Message)
			}
		}
		if err := mergeMeasureScript.Run(ctx, r.rdb, []string{redisMeasureKey(bk)}, args...).Err(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("redis merge measures: %w", err)
		}
	}
	return firstErr
}

// probeTimings sums the workload's buckets that start at or after since. A
// bucket straddling since would mix in requests from before a probe existed.
func (r *tracesRedis) probeTimings(ctx context.Context, namespace, kind, name string, since, now time.Time) (measure.Timings, error) {
	if floor := now.Add(-maxMeasureWindow); since.Before(floor) {
		since = floor
	}
	pipe := r.rdb.Pipeline()
	var cmds []*redis.MapStringStringCmd
	for b := (since.Unix() + measure.BucketSeconds - 1) / measure.BucketSeconds * measure.BucketSeconds; b <= now.Unix(); b += measure.BucketSeconds {
		cmds = append(cmds, pipe.HGetAll(ctx, redisMeasureKey(measureBucket{namespace, kind, name, b})))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("read measures: %w", err)
	}
	out := measure.Timings{}
	for _, cmd := range cmds {
		addMeasureFields(out, cmd.Val())
	}
	for _, tx := range out {
		tx.Entry.SortErrors()
		for _, m := range tx.Sites {
			m.SortErrors()
		}
	}
	return out, nil
}

// addMeasureFields sums one bucket hash into out. Fields that do not parse
// are skipped.
func addMeasureFields(out measure.Timings, fields map[string]string) {
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		raw := fields[field]
		first, last := strings.Index(field, "\x1f"), strings.LastIndex(field, "\x1f")
		n, err := strconv.ParseInt(raw, 10, 64)
		if first < 0 || first == last || last == len(field)-1 || err != nil {
			continue
		}
		txID, site, metric := field[:first], field[first+1:last], field[last+1:]
		tx := out[txID]
		if tx == nil {
			tx = &measure.Transaction{Sites: map[string]*measure.Measure{}}
			out[txID] = tx
		}
		m := &tx.Entry
		if site != "" {
			if m = tx.Sites[site]; m == nil {
				m = &measure.Measure{}
				tx.Sites[site] = m
			}
		}
		switch metric {
		case "n":
			m.N += n
		case "err":
			m.Err += n
		case "sum":
			m.SumUs += n
		case "max":
			m.MaxUs = max(m.MaxUs, n)
		case "self":
			m.SelfUs += n
		case "spans":
			m.Spans += n
		default:
			switch k, convErr := strconv.Atoi(metric[1:]); {
			case convErr != nil:
			case metric[0] == 'h':
				m.Hist.Ensure(histogram.DefaultLayout())
				if k < len(m.Hist.Buckets) {
					m.Hist.Buckets[k] += uint64(n)
				}
			case metric[0] == 'c':
				m.AddError(fields[field[:last+1]+"m"+metric[1:]], n)
			}
		}
	}
}

// measureReader reads the measurements every replica merged into Redis.
type measureReader struct {
	redis *tracesRedis
}

// newMeasureReader builds the Redis client without dialing, as the call tree
// flusher does: an unreachable Redis only fails the reads made meanwhile.
func newMeasureReader(redisEndpoint string, log *commonlogger.OdigosLogger) *measureReader {
	return &measureReader{redis: newTracesRedis(redisEndpoint, log)}
}

// ProbeTimings reads the workload's probe and entry measurements since the
// given time, at most the last ten minutes.
func (r *measureReader) ProbeTimings(ctx context.Context, source store.SourceKey, since time.Time) (measure.Timings, error) {
	return r.redis.probeTimings(ctx, source.Namespace, source.WorkloadKind, source.WorkloadName, since, time.Now())
}

func (r *measureReader) Close() error {
	return r.redis.Close()
}
