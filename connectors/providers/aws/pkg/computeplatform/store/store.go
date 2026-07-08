// Package store (computeplatformstore) is the COMPUTE-PLATFORM inventory persistence for the AWS connector — managed
// infrastructure Odigos is installed on, not instrumented (ECS-EC2 clusters, Fargate clusters; future
// EKS/EC2). Compute platforms have no source/destination intent. It runs over the shared base store's connection
// pool; the sibling workload store owns the directly-instrumentable inventory.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5"

	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var inventoryLog = connlog.Subsystem("aws-computeplatform-store")

// Store is the compute-platform inventory store, embedding the base store for the shared pool + connector identity.
type Store struct {
	*basestore.Store
}

// New builds the compute-platform store over an already-open base store (pool shared with the workload store).
func New(b *basestore.Store) *Store { return &Store{Store: b} }

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
