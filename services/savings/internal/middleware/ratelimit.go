// Minimal in-process per-key token-bucket rate limiter.
//
// Used by the public guarantor-consent endpoints to defend against
// token enumeration + OTP brute-force. NOT a distributed limiter —
// when savings scales horizontally, swap for Redis. For the single-
// instance dev + smallish-tenant production targets this is enough.
//
// Algorithm: classic token bucket. Each key has `capacity` tokens
// and refills at `refillRate` per second. A request consumes 1
// token; empty bucket → 429.

package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/savings/internal/httpx"
)

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

type RateLimiter struct {
	capacity   float64
	refillRate float64 // tokens per second
	keyer      func(*http.Request) string
	mu         sync.Mutex
	buckets    map[string]*bucket
}

// NewRateLimiter — capacity is the burst; refillRate is tokens/sec.
// Example: NewRateLimiter(10, 0.5, ...) = 10 burst, sustained 1 req
// every 2 seconds per key.
func NewRateLimiter(capacity, refillRatePerSec float64, keyer func(*http.Request) string) *RateLimiter {
	return &RateLimiter{
		capacity: capacity, refillRate: refillRatePerSec,
		keyer: keyer, buckets: map[string]*bucket{},
	}
}

func (l *RateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, lastRefill: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * l.refillRate
	if b.tokens > l.capacity {
		b.tokens = l.capacity
	}
	b.lastRefill = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Middleware wraps a Chi route group. Returns 429 with a friendly
// JSON body when rate-limited.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := l.keyer(r)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !l.allow(key) {
			httpx.WriteErr(w, r, &httpx.APIError{
				Status:  http.StatusTooManyRequests,
				Code:    httpx.CodeRateLimited,
				Message: "Too many requests. Please try again in a few seconds.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// KeyByIP returns the client's IP (X-Forwarded-For first, then
// RemoteAddr). Empty when unparseable — the limiter treats empty as
// "no key" and skips, which is safe.
func KeyByIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First IP in the chain — the originator per the standard.
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// KeyByURLParam returns the value of a chi URL parameter — used to
// rate-limit per-token so a single token can't be hammered.
func KeyByURLParam(name string) func(*http.Request) string {
	return func(r *http.Request) string {
		return chi.URLParam(r, name)
	}
}
