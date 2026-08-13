package odigosebpfreceiver

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilium/ebpf"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// metricsReadLoop handles periodic collection of metrics from HashOfMaps and AttributesMap
func (r *ebpfReceiver) metricsReadLoop(ctx context.Context, hashOfMaps *ebpf.Map, attributesMap *ebpf.Map) error {
	interval := r.config.MetricsConfig.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	r.logger.Info("starting JVM metrics collection loop", zap.Duration("interval", interval))

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("metrics collection loop stopping")
			return nil
		case <-ticker.C:
			if err := r.collectMetrics(ctx, hashOfMaps, attributesMap); err != nil {
				r.logger.Error("failed to collect metrics", zap.Error(err))
			}
		}
	}
}

// processEntry is one row snapshotted from the outer HashOfMaps.
type processEntry struct {
	key        [64]byte
	innerMapID uint32
}

// collectMetrics reads the HashOfMaps and the AttributesMap and emits one metric
// batch per instrumented process. It uses a three-phase approach:
//  1. Snapshot the HashOfMaps (UUID -> inner map id)
//  2. Build a UUID -> packed attributes cache from the AttributesMap
//  3. Join the two and process each inner map
//
// Phase order is load-bearing, see the comment in phase 1.
func (r *ebpfReceiver) collectMetrics(ctx context.Context, hashOfMaps *ebpf.Map, attributesMap *ebpf.Map) error {
	// Phase 1: snapshot the outer map (the pointers) BEFORE reading the attributes.
	//
	// This is the mirror of the writer's order in jvmmetrics.addOuterMapEntry, which
	// writes the attributes before publishing the inner map. Snapshotting pointers
	// first and attributes second means every UUID we end up processing had its
	// attributes written before it was published, and we read the attributes after
	// that - so the join cannot miss.
	var (
		processKey [64]byte
		innerMapID uint32
		entries    []processEntry
	)

	iter := hashOfMaps.Iterate()
	for iter.Next(&processKey, &innerMapID) {
		entries = append(entries, processEntry{key: processKey, innerMapID: innerMapID})
	}
	if err := iter.Err(); err != nil {
		r.logger.Error("hash of maps iterator error", zap.Error(err))
	}

	// Phase 2: build the UUID -> packed attributes cache.
	attrCache := make(map[[64]byte]string, len(entries))

	var uuidKey [64]byte
	var attrValue [attrValueMaxSize]byte

	attrIter := attributesMap.Iterate()
	for attrIter.Next(&uuidKey, &attrValue) {
		attrStr := string(bytes.TrimRight(attrValue[:], "\x00"))
		if attrStr != "" {
			attrCache[uuidKey] = attrStr
		}
	}
	if err := attrIter.Err(); err != nil {
		r.logger.Error("attributes map iterator error", zap.Error(err))
	}

	r.logger.Debug("metrics snapshot taken",
		zap.Int("processes", len(entries)),
		zap.Int("attr_cache_entries", len(attrCache)))

	// Phase 3: join and process.
	processedMaps := 0
	seenProcesses := make(map[[64]byte]struct{}, len(entries))

	for _, e := range entries {
		seenProcesses[e.key] = struct{}{}

		// Get or create the start time for this process
		startTime, known := r.processStartTimes[e.key]
		if !known {
			startTime = pcommon.NewTimestampFromTime(time.Now())
			r.processStartTimes[e.key] = startTime
		}

		// Get the inner map from the ID
		innerMap, err := ebpf.NewMapFromID(ebpf.MapID(e.innerMapID))
		if err != nil {
			r.logger.Error("failed to get inner map",
				zap.Uint32("inner_map_id", e.innerMapID),
				zap.Error(err))
			continue
		}

		// Look up cached attributes for this UUID
		packedAttributes, found := attrCache[e.key]
		if !found && r.shouldReportAttrIssue(e.key, attrIssueNoAttributes) {
			// With the snapshot order above, a live process should always be found.
			// If this fires the pair is genuinely out of sync: either the attributes
			// write failed, or the process was torn down between the two snapshots.
			r.logger.Warn("no attributes found for UUID in attributes map",
				zap.String("uuid", uuidString(e.key)),
				zap.Bool("first_seen", !known),
				zap.Uint32("inner_map_id", e.innerMapID),
				zap.Int("attr_cache_entries", len(attrCache)))
		}

		// Process metrics from this inner map
		err = r.processInnerMapMetrics(ctx, innerMap, packedAttributes, startTime, e.key)
		// NewMapFromID hands back a fresh fd every tick; without this the receiver
		// leaks one fd per process per collection interval.
		if closeErr := innerMap.Close(); closeErr != nil {
			r.logger.Debug("failed to close inner map", zap.Error(closeErr))
		}
		if err != nil {
			r.logger.Error("failed to process inner map metrics",
				zap.Uint32("inner_map_id", e.innerMapID),
				zap.Error(err))
			continue
		}

		processedMaps++
	}

	// Cleanup per-process state for processes that are no longer present, so
	// neither map grows with pod churn.
	for key := range r.processStartTimes {
		if _, seen := seenProcesses[key]; !seen {
			delete(r.processStartTimes, key)
		}
	}
	for key := range r.processAttrIssues {
		if _, seen := seenProcesses[key]; !seen {
			delete(r.processAttrIssues, key)
		}
	}

	r.logger.Debug("metrics collection completed",
		zap.Int("inner_maps_found", len(entries)),
		zap.Int("maps_processed", processedMaps))
	return nil
}

// uuidString renders a UUID map key for logging.
func uuidString(key [64]byte) string {
	return string(bytes.TrimRight(key[:], "\x00"))
}

// countMetrics returns how many metrics a resource batch carries across all scopes.
func countMetrics(rm pmetric.ResourceMetrics) int {
	total := 0
	for i := 0; i < rm.ScopeMetrics().Len(); i++ {
		total += rm.ScopeMetrics().At(i).Metrics().Len()
	}
	return total
}

// attrIssue names a per-process resource attribute problem. Used to report on
// state change only.
type attrIssue string

const (
	attrIssueNone          attrIssue = ""
	attrIssueNoAttributes  attrIssue = "no_attributes"
	attrIssueNoServiceName attrIssue = "no_service_name"
)

// shouldReportAttrIssue reports whether this process's attribute state has changed
// since the last collection interval, and records the new state.
//
// Collection runs on a ticker, so a persistent cause - a truncated attribute blob,
// say - would otherwise log once per process per interval forever. On a node with
// many instrumented JVMs that is a flood, not a diagnostic. Gating on transitions
// means a problem logs once when it starts and once when it clears.
func (r *ebpfReceiver) shouldReportAttrIssue(key [64]byte, issue attrIssue) bool {
	if r.processAttrIssues[key] == issue {
		return false
	}
	if issue == attrIssueNone {
		delete(r.processAttrIssues, key)
	} else {
		r.processAttrIssues[key] = issue
	}
	return true
}

// attrKeysCapped renders the keys of a packed "k:v,k:v" blob for logging, bounded
// so an unusual resource cannot produce an unbounded log line. Attribute values are
// customer data and are deliberately excluded.
func attrKeysCapped(packed string) string {
	const maxLen = 512
	var keys []string
	for _, part := range strings.Split(packed, ",") {
		k, _, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	s := strings.Join(keys, ",")
	if len(s) <= maxLen {
		return s
	}
	// Cut on a key boundary so the result stays valid UTF-8 for non-ASCII keys.
	if i := strings.LastIndexByte(s[:maxLen], ','); i > 0 {
		return s[:i] + ",...(truncated)"
	}
	return "...(truncated)"
}

// processInnerMapMetrics processes a single inner map and extracts metrics.
// packedAttributes contains the resource attributes in "key1:value1,key2:value2" format,
// looked up from the AttributesMap by UUID.
// startTime is when this process was first observed, used as StartTimestamp for cumulative metrics.
func (r *ebpfReceiver) processInnerMapMetrics(ctx context.Context, innerMap *ebpf.Map, packedAttributes string, startTime pcommon.Timestamp, processKey [64]byte) error {
	// Process with JVM handler
	metrics, err := r.jvmHandler.ExtractJVMMetricsFromInnerMap(ctx, innerMap, startTime)
	if err != nil {
		return fmt.Errorf("JVM handler failed: %w", err)
	}

	if metrics.ResourceMetrics().Len() == 0 {
		return nil
	}

	// Add resource attributes from packedAttributes if we have metrics
	resourceMetrics := metrics.ResourceMetrics().At(0)
	resourceAttrs := resourceMetrics.Resource().Attributes()
	if packedAttributes != "" {
		if err := r.parseResourceAttributes(resourceAttrs, packedAttributes, ",", ":"); err != nil {
			r.logger.Error("failed to add resource attributes", zap.Error(err))
			return err
		}
	}

	// A batch without service.name cannot be attributed to a workload downstream.
	// This is the symptom the phase ordering in collectMetrics exists to prevent, so
	// log loudly enough to tell the remaining causes apart: an empty packed blob
	// means the join still failed, a non-empty one means the blob itself was bad
	// (truncated past service.name, empty value, or a comma inside a value).
	//
	// Only worth reporting when the batch actually carries datapoints - the handler
	// always appends a ResourceMetrics, so an idle process yields an empty one.
	//
	// Reported on state change only, and with attribute keys rather than values:
	// values are customer data, and the diagnosis needs only which keys survived.
	if datapoints := countMetrics(resourceMetrics); datapoints > 0 {
		_, hasServiceName := resourceAttrs.Get(serviceNameKey)
		switch {
		case !hasServiceName && r.shouldReportAttrIssue(processKey, attrIssueNoServiceName):
			r.logger.Warn("forwarding JVM metrics without service.name",
				zap.String("uuid", uuidString(processKey)),
				zap.Bool("packed_attributes_empty", packedAttributes == ""),
				zap.Int("packed_len", len(packedAttributes)),
				zap.Int("resource_attrs", resourceAttrs.Len()),
				zap.Int("metrics", datapoints),
				zap.String("attr_keys", attrKeysCapped(packedAttributes)))
		case hasServiceName && r.shouldReportAttrIssue(processKey, attrIssueNone):
			r.logger.Info("JVM metrics resource attributes recovered",
				zap.String("uuid", uuidString(processKey)),
				zap.Int("resource_attrs", resourceAttrs.Len()))
		}
	}

	// Forward metrics to consumer if consumer is not nil and there are metrics to forward
	if r.nextMetrics != nil {
		if err := r.nextMetrics.ConsumeMetrics(ctx, metrics); err != nil {
			return fmt.Errorf("failed to consume metrics: %w", err)
		}
	}

	return nil
}

// parseResourceAttributes parses a delimited string and adds resource attributes
// Format: "key1:value1,key2:value2,key3:value3"
// This handles the specific format used by Odigos for eBPF resource attribute encoding
func (r *ebpfReceiver) parseResourceAttributes(resourceAttrs pcommon.Map, attributesStr string, itemDelimiter string, keyValueSeparator string) error {
	if attributesStr == "" {
		return fmt.Errorf("empty attributes string")
	}

	parsed := false
	dropped := 0
	for _, part := range strings.Split(attributesStr, itemDelimiter) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		attrParts := strings.SplitN(part, keyValueSeparator, 2)
		if len(attrParts) != 2 {
			// A comma inside an attribute value produces a fragment with no
			// separator here, and takes the value it was part of down with it.
			dropped++
			continue
		}

		attrKey := strings.TrimSpace(attrParts[0])
		attrValue := strings.TrimSpace(attrParts[1])

		if attrKey != "" && attrValue != "" {
			resourceAttrs.PutStr(attrKey, attrValue)
			parsed = true
			continue
		}

		// An attribute with an empty value is silently invisible downstream, and
		// service.name is the one that matters.
		dropped++
	}

	// Aggregated rather than per-segment, and gated on the level actually being
	// enabled: the logs pipeline calls this once per log record, so a per-segment
	// line floods, and Check keeps the fields unallocated when debug is off. The
	// actionable signal for metrics is the throttled service.name warning in
	// processInnerMapMetrics, which carries the same key list.
	if dropped > 0 {
		if ce := r.logger.Check(zapcore.DebugLevel, "resource attributes parsed with drops"); ce != nil {
			ce.Write(
				zap.Int("dropped", dropped),
				zap.Int("kept", resourceAttrs.Len()),
				zap.String("attr_keys", attrKeysCapped(attributesStr)),
			)
		}
	}

	if !parsed {
		return fmt.Errorf("no valid attributes parsed from: %s", attributesStr)
	}

	return nil
}
