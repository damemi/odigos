package engine

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// anomalySignature is a stable identity for a class of identical anomaly firings
// on one transaction, so repeated firings dedup into one issue instead of one
// stored row per trace. It hashes the transaction, the sorted triggered classes,
// and the offending items each class's evidence flags as new - the parts that
// make two firings "the same finding". Trace id, timestamp, and score vary per
// firing and are deliberately excluded. Returns the hex signature and a short
// human-readable offending summary for the feed.
func anomalySignature(transactionID int64, deviated []repo.DeviationClass, evidence map[string]json.RawMessage) (signature, offending string) {
	classes := make([]string, len(deviated))
	for i, c := range deviated {
		classes[i] = string(c)
	}
	sort.Strings(classes)

	var parts []string
	for _, c := range classes {
		if items := offendingItems(evidence[c]); len(items) > 0 {
			parts = append(parts, c+"="+strings.Join(items, ","))
		}
	}

	h := sha1.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s", transactionID, strings.Join(classes, ","), strings.Join(parts, ";"))
	signature = hex.EncodeToString(h.Sum(nil))

	offending = strings.Join(parts, "; ")
	if offending == "" {
		offending = strings.Join(classes, ", ")
	}
	return signature, offending
}

// offendingItems extracts the canonical, sorted list of items a class's evidence
// flags as new. Every scorer reports its deviations under "new_*" keys
// (new_peers, new_edges, new_accesses, new_libs, ...) whose values are arrays of
// small objects. Reading that convention generically keeps the signature free of
// per-class coupling: a new scorer's deviations dedup correctly as long as it
// follows it. Each item is re-encoded with sorted keys so field order can't split
// one finding into two.
func offendingItems(ev json.RawMessage) []string {
	if len(ev) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(ev, &fields); err != nil {
		return nil
	}
	var items []string
	for key, val := range fields {
		if !strings.HasPrefix(key, "new_") {
			continue
		}
		var arr []any
		if err := json.Unmarshal(val, &arr); err != nil {
			continue
		}
		for _, elem := range arr {
			if canon, err := json.Marshal(elem); err == nil {
				items = append(items, string(canon))
			}
		}
	}
	sort.Strings(items)
	return items
}
