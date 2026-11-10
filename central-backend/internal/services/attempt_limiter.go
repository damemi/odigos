package services

import (
	"sync"
	"time"
)

// attemptLimiter counts failed attempts per key in fixed windows. It is in
// memory, so each central-backend replica keeps its own counts.
type attemptLimiter struct {
	mu        sync.Mutex
	window    time.Duration
	now       func() time.Time
	failures  map[string]attemptWindow
	lastSweep time.Time
}

type attemptWindow struct {
	start time.Time
	count int
}

// attemptKey is one counter a failure is charged to, with its own limit.
type attemptKey struct {
	key   string
	limit int
}

const (
	// sweepThreshold is the map size above which expired windows are dropped.
	sweepThreshold = 4096
	// maxTrackedKeys bounds memory: new keys are refused once it is reached.
	maxTrackedKeys = 100_000
)

func newAttemptLimiter(window time.Duration) *attemptLimiter {
	return &attemptLimiter{
		window:   window,
		now:      time.Now,
		failures: make(map[string]attemptWindow),
	}
}

// blocked reports whether any key has reached its limit in its window, or the
// limiter is full and a key is not tracked yet.
func (l *attemptLimiter) blocked(keys ...attemptKey) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)
	for _, k := range keys {
		w, ok := l.failures[k.key]
		if !ok || now.Sub(w.start) >= l.window {
			if len(l.failures) >= maxTrackedKeys {
				return true
			}
			continue
		}
		if w.count >= k.limit {
			return true
		}
	}
	return false
}

// fail records one failed attempt against every key.
func (l *attemptLimiter) fail(keys ...attemptKey) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for _, k := range keys {
		w, ok := l.failures[k.key]
		if !ok || now.Sub(w.start) >= l.window {
			if !ok && len(l.failures) >= maxTrackedKeys {
				continue
			}
			w = attemptWindow{start: now}
		}
		w.count++
		l.failures[k.key] = w
	}
}

// reset forgets the failures recorded against key.
func (l *attemptLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

func (l *attemptLimiter) sweep(now time.Time) {
	if len(l.failures) < sweepThreshold || now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k, w := range l.failures {
		if now.Sub(w.start) >= l.window {
			delete(l.failures, k)
		}
	}
}
