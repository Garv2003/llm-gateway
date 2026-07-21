// Package ratelimit provides per-API-key rate limiting for incoming requests.
//
// The Limiter interface is deliberately small so the backend can be swapped
// without touching the middleware. The default LocalLimiter keeps token buckets
// in process memory, which is correct for a single gateway instance.
//
// Composition path for a horizontally scaled deployment: a second
// implementation, RemoteLimiter, would satisfy the same interface by calling
// the standalone distributed-rate-limiter service over gRPC, so limits are
// shared across every gateway instance rather than counted per process. That
// module is intentionally not imported here to keep this build free of the
// private dependency; the interface is all that the middleware needs, and the
// remote backend slots in by constructing a different Limiter in main.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Limiter decides whether a request identified by key may proceed. When a
// request is denied, retryAfter reports how long the caller should wait before
// the next token becomes available. err is non-nil only for backend failures
// (e.g. a RemoteLimiter losing its gRPC connection); the LocalLimiter never
// returns an error.
type Limiter interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

// LocalLimiter is an in-memory token-bucket limiter with one bucket per key.
// Each bucket refills at rps tokens per second up to a maximum of burst tokens.
// It is safe for concurrent use.
type LocalLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	rps   float64
	burst float64

	// now is overridable in tests to exercise refill deterministically.
	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLocal builds a LocalLimiter that allows rps requests per second per key
// with a burst allowance of burst requests. A burst below 1 is raised to 1 so a
// single request can always pass when the bucket is full.
func NewLocal(rps float64, burst int) *LocalLimiter {
	b := float64(burst)
	if b < 1 {
		b = 1
	}
	return &LocalLimiter{
		buckets: make(map[string]*bucket),
		rps:     rps,
		burst:   b,
		now:     time.Now,
	}
}

// Allow consumes one token from key's bucket. It returns allowed=true when a
// token was available; otherwise allowed=false and the duration until the
// bucket accrues one token.
func (l *LocalLimiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	// Refill by the time elapsed since the bucket was last touched, capped at
	// the burst ceiling.
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * l.rps
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0, nil
	}

	// Not enough for a full token: report the wait until one accrues.
	var retryAfter time.Duration
	if l.rps > 0 {
		retryAfter = time.Duration((1 - b.tokens) / l.rps * float64(time.Second))
	}
	return false, retryAfter, nil
}
