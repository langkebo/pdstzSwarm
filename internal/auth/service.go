package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultOAuthHTTPTimeout caps every upstream call to the IdP. We
// keep it small so a stuck Google / GitHub doesn't block the user's
// callback indefinitely.
const defaultOAuthHTTPTimeout = 8 * time.Second

// defaultHTTPClient is a package-level client with the timeout
// above. OAuth flows are short-lived; reusing a single client lets
// us benefit from keep-alive.
var defaultHTTPClient = &http.Client{Timeout: defaultOAuthHTTPTimeout}

// httpPostForm sends a POST with an `application/x-www-form-urlencoded`
// body. Optional `accept` overrides the Accept header (GitHub wants
// `application/json` to avoid URL-encoded responses).
func httpPostForm(ctx context.Context, endpoint string, form url.Values, accept ...string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if len(accept) > 0 && accept[0] != "" {
		req.Header.Set("Accept", accept[0])
	}
	return do(req)
}

// httpGetBearer sends a GET with the supplied bearer token in the
// Authorization header.
func httpGetBearer(ctx context.Context, endpoint, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	return do(req)
}

func do(req *http.Request) ([]byte, error) {
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("oauth: %s %s → %d: %s",
			req.Method, req.URL.Host, resp.StatusCode, string(body))
	}
	return body, nil
}

// ---------- Service ----------

// Service glues the user store, session store, OAuth state store,
// and provider registry into a single object the handler can call.
//
// The handler does no business logic — it just translates HTTP into
// Service calls and back.
type Service struct {
	Users       UserStore
	Sessions    SessionStore
	OAuthStates OAuthStateStore
	Providers   *ProviderRegistry
	// Now lets tests inject a fixed clock. Production: time.Now.
	Now func() time.Time
	// IssueSession optionally allows callers to skip the default
	// session issuing (e.g. for tests). When nil, the default
	// implementation is used.
	IssueSession func(user *User, remember bool) (*Session, error)
}

// NewService wires a service with the default in-memory stores.
func NewService() *Service {
	now := func() time.Time { return time.Now().UTC() }
	return &Service{
		Users:       NewInMemoryUserStore(),
		Sessions:    NewInMemorySessionStore(now),
		OAuthStates: NewInMemoryOAuthStateStore(now),
		Providers:   NewProviderRegistry(),
		Now:         now,
	}
}

// AuthenticatePassword looks up the user by username and verifies
// the password. Returns ErrInvalidCredentials on any failure (we
// don't disclose whether the user exists).
func (s *Service) AuthenticatePassword(username, password string) (*User, error) {
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	u, err := s.Users.GetByUsername(username)
	if err != nil {
		// burn a constant amount of time to avoid timing oracles
		// (compare against a fake hash)
		_ = HashPassword(password, "burn")
		return nil, ErrInvalidCredentials
	}
	if err := VerifyPassword(u, password); err != nil {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// LoginWithPassword authenticates and issues a session. Returns the
// session and the user.
func (s *Service) LoginWithPassword(username, password string, remember bool) (*Session, *User, error) {
	u, err := s.AuthenticatePassword(username, password)
	if err != nil {
		return nil, nil, err
	}
	sess, err := s.issueSession(u, remember)
	if err != nil {
		return nil, nil, err
	}
	_ = s.Users.UpdateLastSeen(u.ID, s.Now())
	return sess, u, nil
}

// issueSession creates and stores a session for the user.
func (s *Service) issueSession(u *User, remember bool) (*Session, error) {
	if s.IssueSession != nil {
		return s.IssueSession(u, remember)
	}
	now := s.Now()
	ttl := DefaultSessionTTL
	if remember {
		ttl = RememberedSessionTTL
	}
	sess := &Session{
		ID:         RandomID(),
		UserID:     u.ID,
		TenantID:   u.TenantID, // P5+ 商业化 - 把 user.tenant_id 复制到 session 让 RLS 路径无需再 JOIN
		Username:   u.Username,
		Role:       u.Role,
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
		LastSeenAt: now,
	}
	if err := s.Sessions.Put(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// AuthenticateSession looks up a session by id, returns the user.
// Updates LastSeenAt as a side effect.
func (s *Service) AuthenticateSession(sessionID string) (*User, *Session, error) {
	if sessionID == "" {
		return nil, nil, ErrSessionNotFound
	}
	sess, err := s.Sessions.Get(sessionID)
	if err != nil {
		return nil, nil, err
	}
	u, err := s.Users.GetByID(sess.UserID)
	if err != nil {
		return nil, nil, err
	}
	_ = s.Sessions.Touch(sessionID, s.Now())
	return u, sess, nil
}

// Logout invalidates a single session. Idempotent.
func (s *Service) Logout(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return s.Sessions.Delete(sessionID)
}

// LogoutEverywhere invalidates every session for the user.
func (s *Service) LogoutEverywhere(userID string) (int, error) {
	return s.Sessions.DeleteByUser(userID)
}

// ---------- OAuth ----------

// BeginOAuth returns the URL to redirect the user to, plus the
// generated state token (the handler sets this in a short-lived
// cookie too).
func (s *Service) BeginOAuth(providerName, redirectAfter string) (string, *OAuthState, error) {
	p := s.Providers.Get(providerName)
	if p == nil {
		return "", nil, fmt.Errorf("oauth: unknown provider %q", providerName)
	}
	state := &OAuthState{
		Token:     RandomID(),
		Provider:  providerName,
		Redirect:  redirectAfter,
		CreatedAt: s.Now(),
	}
	if err := s.OAuthStates.Put(state); err != nil {
		return "", nil, err
	}
	return state.Token, state, nil
}

// BuildAuthURL builds the IdP authorization URL for an in-flight
// state. The handler is responsible for resolving the absolute
// redirect URI (e.g. `${BASE_URL}/api/v1/auth/oauth/callback`).
func (s *Service) BuildAuthURL(providerName, stateToken, redirectURI string) (string, error) {
	p := s.Providers.Get(providerName)
	if p == nil {
		return "", fmt.Errorf("oauth: unknown provider %q", providerName)
	}
	return p.AuthURL(stateToken, redirectURI), nil
}

// CompleteOAuth consumes the state, exchanges the code, fetches
// the profile, finds-or-creates a local user, and issues a session.
func (s *Service) CompleteOAuth(ctx context.Context, stateToken, code, redirectURI string) (*Session, *User, error) {
	st, err := s.OAuthStates.Consume(stateToken)
	if err != nil {
		return nil, nil, err
	}
	p := s.Providers.Get(st.Provider)
	if p == nil {
		return nil, nil, fmt.Errorf("oauth: provider %q vanished between Begin and Complete", st.Provider)
	}
	tok, err := p.Exchange(ctx, code, redirectURI)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth: exchange: %w", err)
	}
	profile, err := p.FetchProfile(ctx, tok.AccessToken)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth: profile: %w", err)
	}
	if profile.ID == "" {
		return nil, nil, fmt.Errorf("oauth: provider %q returned empty profile id", st.Provider)
	}
	u, err := s.Users.GetByProvider(st.Provider, profile.ID)
	if err != nil {
		// First login — create a local user. We hash the user ID
		// into a username fallback in case the provider didn't
		// supply one.
		username := profile.Username
		if username == "" {
			username = st.Provider + "_" + truncateForUsername(profile.ID, 8)
		}
		// Disambiguate against existing usernames by suffixing a
		// short hash if necessary.
		if _, err := s.Users.GetByUsername(username); err == nil {
			username = username + "_" + truncateForUsername(profile.ID, 4)
		}
		u = &User{
			ID:          RandomID(),
			Username:    username,
			Email:       profile.Email,
			DisplayName: profile.DisplayName,
			AvatarURL:   profile.AvatarURL,
			Role:        RoleOperator,
			Provider:    st.Provider,
			ProviderID:  profile.ID,
		}
		if err := s.Users.Create(u); err != nil {
			return nil, nil, fmt.Errorf("oauth: create user: %w", err)
		}
	}
	sess, err := s.issueSession(u, true)
	if err != nil {
		return nil, nil, err
	}
	_ = s.Users.UpdateLastSeen(u.ID, s.Now())
	return sess, u, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// truncateForUsername returns the first n characters of s, or the
// full string if it's shorter. Safe for empty inputs.
func truncateForUsername(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// HasProvider returns true iff a provider with the given name is
// registered. The login page uses this to decide which OAuth
// buttons to render.
func (s *Service) HasProvider(name string) bool {
	return s.Providers.Get(name) != nil
}

// Stop tears down background goroutines. Idempotent.
func (s *Service) Stop() {
	if iss, ok := s.Sessions.(*InMemorySessionStore); ok {
		iss.Stop()
	}
	if oss, ok := s.OAuthStates.(*InMemoryOAuthStateStore); ok {
		oss.Stop()
	}
}
