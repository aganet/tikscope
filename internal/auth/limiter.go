package auth

import (
	"sync"
	"time"
)

// limiter slows down credential guessing: after maxFails failures a key
// (client address or username) is locked for a delay that doubles with
// every further failure, up to maxLock.
type limiter struct {
	maxFails int
	baseLock time.Duration
	maxLock  time.Duration
	forget   time.Duration

	mu        sync.Mutex
	entries   map[string]*attempts
	lastSweep time.Time
}

type attempts struct {
	fails       int
	last        time.Time
	lockedUntil time.Time
}

func newLimiter() *limiter {
	return &limiter{maxFails: 5, baseLock: 30 * time.Second, maxLock: time.Hour, forget: time.Hour, entries: map[string]*attempts{}}
}

// retryAfter reports how long the key stays locked; zero means allowed.
func (l *limiter) retryAfter(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	a := l.entries[key]
	if a == nil || !now.Before(a.lockedUntil) {
		return 0
	}
	return a.lockedUntil.Sub(now)
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.entries[key]
	if a == nil {
		a = &attempts{}
		l.entries[key] = a
	}
	a.fails++
	a.last = now
	if a.fails >= l.maxFails {
		lock := l.baseLock << min(a.fails-l.maxFails, 12)
		a.lockedUntil = now.Add(min(lock, l.maxLock))
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// sweep must be called with l.mu held.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k, a := range l.entries {
		if now.After(a.lockedUntil) && now.Sub(a.last) > l.forget {
			delete(l.entries, k)
		}
	}
}
