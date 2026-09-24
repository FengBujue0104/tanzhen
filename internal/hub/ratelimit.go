package hub

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// LoginLimiter is a fixed-window attempt counter keyed by client IP. It exists
// so the admin password cannot be ground down by an online attacker, which
// matters more than usual because the password may also be the only API secret.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptBucket
	limit    int
	window   time.Duration
	now      func() time.Time
}

type attemptBucket struct {
	count int
	until time.Time
}

func NewLoginLimiter(limit int, window time.Duration) *LoginLimiter {
	if limit <= 0 {
		limit = 5
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &LoginLimiter{attempts: make(map[string]*attemptBucket), limit: limit, window: window, now: time.Now}
}

// Allow reports whether a login attempt from this key may proceed.
func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.attempts[key]
	if !ok {
		return true
	}
	if !l.now().Before(b.until) {
		delete(l.attempts, key)
		return true
	}
	return b.count < l.limit
}

// Fail records a failed attempt, returning true when the key is now locked out.
func (l *LoginLimiter) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.attempts[key]
	if b == nil || !l.now().Before(b.until) {
		b = &attemptBucket{until: l.now().Add(l.window)}
		l.attempts[key] = b
	}
	b.count++
	return b.count >= l.limit
}

// Reset clears a key after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

// RetryAfter seconds a locked-out client should wait, or 0 when not locked.
func (l *LoginLimiter) RetryAfter(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.attempts[key]
	if !ok || !l.now().Before(b.until) {
		return 0
	}
	d := b.until.Sub(l.now())
	if d < 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// clientKey derives the rate-limit key. X-Forwarded-For is honoured only when
// the operator says a trusted proxy is in front, so a spoofed header cannot
// reset someone else's bucket.
func clientKey(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
