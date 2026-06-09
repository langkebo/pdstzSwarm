package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// HashPassword returns a hex-encoded SHA-256 digest of the salt+plain
// concatenation. We use SHA-256 instead of bcrypt to keep the package
// zero-dep; **production deployments should swap this for
// golang.org/x/crypto/bcrypt** (noted in the package doc).
func HashPassword(plain, salt string) string {
	h := sha256.Sum256([]byte(salt + ":" + plain))
	return hex.EncodeToString(h[:])
}

// VerifyPassword returns nil iff the supplied plaintext matches the
// stored hash for the given user.
func VerifyPassword(user *User, plain string) error {
	if user == nil || user.PasswordHash == "" {
		return ErrInvalidCredentials
	}
	// user.ID doubles as the salt. (Stable, unique, non-secret.)
	got := HashPassword(plain, user.ID)
	if !strings.EqualFold(got, user.PasswordHash) {
		return ErrInvalidCredentials
	}
	return nil
}

// ---------- UserStore ----------

// UserStore persists users. Implementations: InMemoryUserStore
// (default) and PostgresUserStore (optional, defined in store_db.go
// when the project decides to wire it up).
type UserStore interface {
	GetByUsername(username string) (*User, error)
	GetByID(id string) (*User, error)
	GetByProvider(provider, providerID string) (*User, error)
	Create(u *User) error
	UpdateLastSeen(id string, t time.Time) error
}

// InMemoryUserStore is the default implementation. It is safe for
// concurrent use. Seed users come from `seed` in NewInMemoryUserStore.
type InMemoryUserStore struct {
	mu    sync.RWMutex
	users map[string]*User // keyed by id
	byU   map[string]string // username -> id
	byP   map[string]string // provider+":"+providerID -> id
}

// NewInMemoryUserStore creates a store pre-seeded with one demo user
// ("operator" / "operator") so the dev experience stays unbroken
// even before the auth migration is run.
func NewInMemoryUserStore() *InMemoryUserStore {
	s := &InMemoryUserStore{
		users: make(map[string]*User),
		byU:   make(map[string]string),
		byP:   make(map[string]string),
	}
	now := time.Now().UTC()
	s.seed(&User{
		ID:           RandomID(),
		Username:     "operator",
		Email:        "operator@pentestswarm.local",
		DisplayName:  "Demo Operator",
		Role:         RoleOperator,
		Provider:     "password",
		PasswordHash: "", // set below
		CreatedAt:    now,
		LastSeenAt:   now,
	})
	return s
}

func (s *InMemoryUserStore) seed(u *User) {
	// default password = "operator" (demo only)
	u.PasswordHash = HashPassword("operator", u.ID)
	s.users[u.ID] = u
	s.byU[strings.ToLower(u.Username)] = u.ID
}

// GetByUsername returns a user by case-insensitive username.
func (s *InMemoryUserStore) GetByUsername(username string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byU[strings.ToLower(username)]
	if !ok {
		return nil, ErrUserNotFound
	}
	u, ok := s.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return cloneUser(u), nil
}

// GetByID returns a user by id.
func (s *InMemoryUserStore) GetByID(id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return cloneUser(u), nil
}

// GetByProvider returns a user by external provider id (e.g. github
// user id). Used by OAuth callback handlers.
func (s *InMemoryUserStore) GetByProvider(provider, providerID string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byP[provider+":"+providerID]
	if !ok {
		return nil, ErrUserNotFound
	}
	u, ok := s.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return cloneUser(u), nil
}

// Create stores a new user. Returns an error if the username is
// already taken.
func (s *InMemoryUserStore) Create(u *User) error {
	if u.ID == "" {
		u.ID = RandomID()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	if u.LastSeenAt.IsZero() {
		u.LastSeenAt = u.CreatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byU[strings.ToLower(u.Username)]; exists {
		return errors.New("username already taken")
	}
	if u.Provider != "" && u.ProviderID != "" {
		if _, exists := s.byP[u.Provider+":"+u.ProviderID]; exists {
			return errors.New("provider id already linked")
		}
	}
	// user.ID doubles as the salt. If a plaintext password is set on
	// the input, hash it before storing.
	if u.PasswordHash == "" && u.Provider == "password" {
		// caller should have set PasswordHash already; nothing to do
	}
	s.users[u.ID] = cloneUser(u)
	s.byU[strings.ToLower(u.Username)] = u.ID
	if u.Provider != "" && u.ProviderID != "" {
		s.byP[u.Provider+":"+u.ProviderID] = u.ID
	}
	return nil
}

// UpdateLastSeen updates the user's last-seen timestamp.
func (s *InMemoryUserStore) UpdateLastSeen(id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return ErrUserNotFound
	}
	u.LastSeenAt = t
	return nil
}

func cloneUser(u *User) *User {
	c := *u
	return &c
}

// ---------- SessionStore ----------

// SessionStore persists sessions. Implementations:
// InMemorySessionStore (default) and a Postgres-backed one (defined
// in store_db.go when wired up).
type SessionStore interface {
	Put(s *Session) error
	Get(id string) (*Session, error)
	Touch(id string, t time.Time) error
	Delete(id string) error
	DeleteByUser(userID string) (int, error)
}

// InMemorySessionStore is the default implementation. Safe for
// concurrent use. A background goroutine prunes expired sessions
// every 15 minutes; call Stop to terminate it.
type InMemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	stopCh   chan struct{}
	Now      func() time.Time
}

// NewInMemorySessionStore creates a session store and starts the
// background pruner. now is the clock source; if nil, time.Now is used.
func NewInMemorySessionStore(now func() time.Time) *InMemorySessionStore {
	if now == nil {
		now = time.Now
	}
	s := &InMemorySessionStore{
		sessions: make(map[string]*Session),
		stopCh:   make(chan struct{}),
		Now:      now,
	}
	go s.pruneLoop()
	return s
}

// Put inserts or updates a session.
func (s *InMemorySessionStore) Put(sess *Session) error {
	if sess.ID == "" {
		return errors.New("session id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = cloneSession(sess)
	return nil
}

// Get returns a session by id, or ErrSessionNotFound. Expired
// sessions are removed as a side-effect.
func (s *InMemorySessionStore) Get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	if sess.IsExpired(s.Now()) {
		delete(s.sessions, id)
		return nil, ErrSessionNotFound
	}
	return cloneSession(sess), nil
}

// Touch updates the session's LastSeenAt timestamp and pushes the
// expiry forward by half the remaining TTL.
func (s *InMemorySessionStore) Touch(id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if sess.IsExpired(s.Now()) {
		delete(s.sessions, id)
		return ErrSessionNotFound
	}
	sess.LastSeenAt = t
	remaining := sess.ExpiresAt.Sub(sess.LastSeenAt)
	if remaining < 5*time.Minute {
		// push expiry forward by half the original TTL
		original := sess.ExpiresAt.Sub(sess.CreatedAt)
		sess.ExpiresAt = t.Add(original / 2)
	}
	return nil
}

// Delete removes a single session.
func (s *InMemorySessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

// DeleteByUser removes every session belonging to a user. Used by
// the "log out everywhere" button.
func (s *InMemorySessionStore) DeleteByUser(userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, id)
			removed++
		}
	}
	return removed, nil
}

// Stop terminates the background pruner. Safe to call multiple times.
func (s *InMemorySessionStore) Stop() {
	select {
	case <-s.stopCh:
		// already closed
	default:
		close(s.stopCh)
	}
}

func (s *InMemorySessionStore) pruneLoop() {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.prune()
		}
	}
}

func (s *InMemorySessionStore) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	for id, sess := range s.sessions {
		if sess.IsExpired(now) {
			delete(s.sessions, id)
		}
	}
}

func cloneSession(s *Session) *Session {
	c := *s
	return &c
}
