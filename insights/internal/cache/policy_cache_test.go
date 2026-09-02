package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// countingPolicyRepo embeds the interface (so unused methods exist) and counts
// how many ListEnabledPolicies calls reach the backing store. err, when set,
// makes the next call fail.
type countingPolicyRepo struct {
	repo.Repository
	mu       sync.Mutex
	calls    int
	policies []repo.Policy
	err      error
}

func (c *countingPolicyRepo) ListEnabledPolicies(_ context.Context) ([]repo.Policy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.policies, nil
}

func (c *countingPolicyRepo) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestPolicyCache_ServesWithinTTL(t *testing.T) {
	backing := &countingPolicyRepo{policies: []repo.Policy{{ID: 1, Enabled: true}}}
	now := time.Unix(0, 0).UTC()
	c := newPolicyCache(backing, 10*time.Second, func() time.Time { return now })
	ctx := context.Background()

	// First call is a miss → hits the backing repo.
	if _, err := c.ListEnabledPolicies(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Many calls inside the TTL are all served from memory.
	for i := 0; i < 100; i++ {
		if _, err := c.ListEnabledPolicies(ctx); err != nil {
			t.Fatalf("cached call: %v", err)
		}
	}
	if backing.callCount() != 1 {
		t.Fatalf("expected 1 backing call within TTL, got %d", backing.callCount())
	}

	// Crossing the TTL triggers exactly one refresh.
	now = now.Add(10 * time.Second)
	if _, err := c.ListEnabledPolicies(ctx); err != nil {
		t.Fatalf("post-TTL call: %v", err)
	}
	if backing.callCount() != 2 {
		t.Fatalf("expected a refresh after the TTL, got %d backing calls", backing.callCount())
	}
}

// TestPolicyCache_ServesStaleOnError proves a transient DB error doesn't blank
// the policy set: once the cache is warm, a failed refresh returns the last
// good snapshot instead of an error.
func TestPolicyCache_ServesStaleOnError(t *testing.T) {
	backing := &countingPolicyRepo{policies: []repo.Policy{{ID: 7, Enabled: true}}}
	now := time.Unix(0, 0).UTC()
	c := newPolicyCache(backing, time.Second, func() time.Time { return now })
	ctx := context.Background()

	got, err := c.ListEnabledPolicies(ctx)
	if err != nil || len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("warm-up: got %v err %v", got, err)
	}

	// Expire the entry and make the next refresh fail.
	now = now.Add(2 * time.Second)
	backing.mu.Lock()
	backing.err = errors.New("db down")
	backing.mu.Unlock()

	got, err = c.ListEnabledPolicies(ctx)
	if err != nil {
		t.Fatalf("expected stale snapshot, got error: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("expected last good snapshot, got %v", got)
	}
}

// TestPolicyCache_ColdErrorPropagates proves that if the very first load fails
// (no snapshot to fall back on), the error is returned rather than masked.
func TestPolicyCache_ColdErrorPropagates(t *testing.T) {
	backing := &countingPolicyRepo{err: errors.New("db down")}
	now := time.Unix(0, 0).UTC()
	c := newPolicyCache(backing, time.Second, func() time.Time { return now })

	if _, err := c.ListEnabledPolicies(context.Background()); err == nil {
		t.Fatal("expected error on cold-cache failure, got nil")
	}
}
