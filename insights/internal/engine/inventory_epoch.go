package engine

import (
	"context"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// The transaction-inventory generation guards write-behind buffers against an
// operator reset. A replica that has not yet noticed "delete all transactions"
// keeps counting against identities that no longer exist, and flushing those
// counts would restore findings the reset just cleared. Every buffered group is
// stamped with the generation its replica believed while counting, and the flush
// drops the groups a reset superseded. Shared by the guardrail-violation and
// anomaly-occurrence buffers.

// liveInventoryEpoch reads the generation from storage. Deliberately not taken
// from a replica's settings snapshot: the snapshot is only what this replica
// believes, and the comparison exists to catch a replica whose belief is stale.
// Cold path - one read per flush interval, skipped when nothing accumulated.
func liveInventoryEpoch(ctx context.Context, r repo.Repository) (int, error) {
	settings, err := r.GetSystemSettings(ctx)
	if err != nil {
		return 0, err
	}
	return settings.TransactionInventoryEpoch, nil
}

// supersededByReset reports whether a group stamped with stamped describes an
// inventory generation that a reset has since replaced.
func supersededByReset(stamped, live int) bool { return stamped != live }
