package cache

import (
	"context"
	"sync"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Metrics:
//   - insights_policy_cache_lookups_total{result="hit|miss"} — hit = served
//     from memory, miss = refreshed from the DB. A miss falls through to the
//     metrics layer, which counts it under
//     insights_repo_calls_total{method="ListEnabledPolicies"}.

// DefaultPolicyTTL is how long enabled-policy results are served from memory
// before the next call refreshes them.
const DefaultPolicyTTL = 10 * time.Second

// WrapPolicies wraps a Repository so ListEnabledPolicies is served from memory
// for DefaultPolicyTTL before refreshing. Applied as the outermost repo layer.
func WrapPolicies(wrapped repo.Repository) repo.Repository {
	return newPolicyCache(wrapped, DefaultPolicyTTL, time.Now)
}

// newPolicyCache is the injectable constructor used by WrapPolicies and tests
// (which pass a small TTL and a controllable clock).
func newPolicyCache(wrapped repo.Repository, ttl time.Duration, clock func() time.Time) *policyCache {
	return &policyCache{Repository: wrapped, ttl: ttl, clock: clock}
}

// policyCache embeds repo.Repository so every other method passes straight
// through to the wrapped repository; only ListEnabledPolicies is cached.
type policyCache struct {
	repo.Repository

	ttl   time.Duration
	clock func() time.Time

	mu        sync.RWMutex
	cached    []repo.Policy
	fetchedAt time.Time
	valid     bool
}

// ListEnabledPolicies returns the cached policy list while it is fresh, and
// otherwise refreshes it from the wrapped repository. The returned slice is
// shared and must be treated as read-only.
func (c *policyCache) ListEnabledPolicies(ctx context.Context) ([]repo.Policy, error) {
	c.mu.RLock()
	cached, fetchedAt, valid := c.cached, c.fetchedAt, c.valid
	c.mu.RUnlock()

	if valid && c.clock().Sub(fetchedAt) < c.ttl {
		metrics.IncPolicyCache("hit")
		return cached, nil
	}

	// Stale or cold: refresh from the DB.
	fresh, err := c.Repository.ListEnabledPolicies(ctx)
	if err != nil {
		// Serve the last good snapshot (if any) so a transient DB error doesn't silence all alerting.
		if valid {
			metrics.IncPolicyCache("hit")
			return cached, nil
		}
		return nil, err
	}

	c.mu.Lock()
	c.cached, c.fetchedAt, c.valid = fresh, c.clock(), true
	c.mu.Unlock()

	metrics.IncPolicyCache("miss")
	return fresh, nil
}
