package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/odigos-io/central-backend/internal/utils"
)

func TestSnapshotQueries_RequireAuthentication(t *testing.T) {
	r := &queryResolver{&Resolver{}}

	if _, err := r.ClusterSnapshot(context.Background(), "cluster-1"); !errors.Is(err, utils.ErrUnauthenticated) {
		t.Fatalf("ClusterSnapshot: expected ErrUnauthenticated, got %v", err)
	}
	if _, err := r.AllClusterSnapshots(context.Background()); !errors.Is(err, utils.ErrUnauthenticated) {
		t.Fatalf("AllClusterSnapshots: expected ErrUnauthenticated, got %v", err)
	}
}
