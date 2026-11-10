// Package store (workloadstore) is the WORKLOAD inventory persistence for the Azure connector —
// Container Apps, the resources the connector instruments directly via a Source. It runs over the
// shared base store's connection pool (sources/destinations/heartbeat live in the base store); the
// sibling compute-platform store owns the managed-infra (AKS/VM) inventory.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5"

	connlog "github.com/odigos-io/odigos/connectors/base/log"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
)

var inventoryLog = connlog.Subsystem("azure-workload-store")

// Store is the workload inventory store. It embeds the base store to reuse the pool + connector identity
// (and, for the instrumentor, the shared source/destination methods).
type Store struct {
	*basestore.Store
}

// New builds the workload store over an already-open base store (the pool is shared with the
// compute-platform store and the base sources/destinations).
func New(b *basestore.Store) *Store { return &Store{Store: b} }

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
