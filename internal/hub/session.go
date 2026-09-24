package hub

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// SessionStore keeps opaque, expiring session ids in memory. Sliding expiry:
// every successful check pushes the deadline out by ttl.
type SessionStore struct {
	mu     sync.Mutex
	byID   map[string]time.Time
	ttl    time.Duration
	maxCap int
	now    func() time.Time
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &SessionStore{byID: make(map[string]time.Time), ttl: ttl, maxCap: 4096, now: time.Now}
}

// Create mints a session id. Returns "" when the table is at capacity.
func (s *SessionStore) Create() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand is documented not to fail; refuse rather than mint a
		// predictable id if it ever does.
		return ""
	}
	id := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byID) >= s.maxCap {
		s.evictLocked(s.now())
		if len(s.byID) >= s.maxCap {
			return ""
		}
	}
	s.byID[id] = s.now().Add(s.ttl)
	return id
}

// Check reports whether an id is live, refreshing its deadline if so.
func (s *SessionStore) Check(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.byID[id]
	if !ok {
		return false
	}
	if !s.now().Before(exp) {
		delete(s.byID, id)
		return false
	}
	s.byID[id] = s.now().Add(s.ttl)
	return true
}

// Revoke drops one session.
func (s *SessionStore) Revoke(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// Len is the current session count.
func (s *SessionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// evictLocked removes already-expired sessions. Caller holds the mutex.
func (s *SessionStore) evictLocked(now time.Time) int {
	n := 0
	for id, exp := range s.byID {
		if !now.Before(exp) {
			delete(s.byID, id)
			n++
		}
	}
	return n
}

// Sweep drops expired sessions. Safe to call periodically.
func (s *SessionStore) Sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictLocked(s.now())
}
