package relayserver

import (
	"sync"
	"time"
)

type rateEntry struct {
	start time.Time
	count int
}
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]rateEntry
	limit   int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{entries: make(map[string]rateEntry), limit: limit}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e := l.entries[key]
	if e.start.IsZero() || now.Sub(e.start) >= time.Minute {
		e = rateEntry{start: now}
	}
	if e.count >= l.limit {
		l.entries[key] = e
		return false
	}
	e.count++
	l.entries[key] = e
	if len(l.entries) > 4096 {
		for k, v := range l.entries {
			if now.Sub(v.start) > 2*time.Minute {
				delete(l.entries, k)
			}
		}
	}
	return true
}
