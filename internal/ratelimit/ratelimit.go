// Package ratelimit implements a per-key sliding-window limiter (requests
// per minute). It is in-process; for multi-node deployments put a shared
// limiter in front (e.g. Redis) or run one instance per shard.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter tracks request timestamps per key.
type Limiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	now     func() time.Time
}

// New returns an empty limiter.
func New() *Limiter {
	return &Limiter{windows: map[string][]time.Time{}, now: time.Now}
}

// Allow records a request for key when the quota is not exhausted.
// It returns whether the request is allowed and the remaining allowance.
func (l *Limiter) Allow(key string, rpm int) (bool, int) {
	if rpm <= 0 {
		rpm = 60
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-time.Minute)
	ts := l.windows[key]
	keep := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= rpm {
		l.windows[key] = keep
		return false, 0
	}
	l.windows[key] = append(keep, now)
	return true, rpm - len(keep) - 1
}

// Reset clears the window for a key (used when a tenant is re-enabled).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.windows, key)
	l.mu.Unlock()
}

// GC drops empty windows; call periodically from a janitor goroutine.
func (l *Limiter) GC() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-2 * time.Minute)
	removed := 0
	for k, ts := range l.windows {
		keep := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			delete(l.windows, k)
			removed++
			continue
		}
		l.windows[k] = keep
	}
	return removed
}
