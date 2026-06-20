package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]bucket
	now     func() time.Time
}

type bucket struct {
	count     int
	resetTime time.Time
}

func New(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:   limit,
		window:  window,
		buckets: map[string]bucket{},
		now:     time.Now,
	}
}

func NewWithClock(limit int, window time.Duration, now func() time.Time) *Limiter {
	limiter := New(limit, window)
	limiter.now = now
	return limiter
}

func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.buckets[key]
	if b.resetTime.IsZero() || !now.Before(b.resetTime) {
		b = bucket{resetTime: now.Add(l.window)}
	}
	if b.count >= l.limit {
		l.buckets[key] = b
		return false, b.resetTime.Sub(now)
	}
	b.count++
	l.buckets[key] = b
	return true, b.resetTime.Sub(now)
}
