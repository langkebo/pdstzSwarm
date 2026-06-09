package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"
)

// Provider implements one OAuth 2.0 / OIDC identity provider. The
// interface is intentionally minimal so new providers (GitLab, Slack,
// WeChat Work, etc.) can be added without touching the handler.
type Provider interface {
	// Name is the stable identifier ("google", "github", "oidc:…").
	Name() string
	// DisplayName is the human-readable label shown on the login page.
	DisplayName() string
	// AuthURL builds the URL to redirect the user to. `state` MUST
	// round-trip through the user's browser and come back via the
	// callback URL.
	AuthURL(state, redirectURI string) string
	// Exchange exchanges a `code` (returned by the IdP) for a token
	// response. Implementations are free to call any HTTP API.
	Exchange(ctx context.Context, code, redirectURI string) (*TokenResponse, error)
	// FetchProfile reads the user's profile (id, email, display name,
	// avatar) using the access token. The returned `Profile.ID` MUST
	// be stable across logins — that's how we match returning users.
	FetchProfile(ctx context.Context, accessToken string) (*Profile, error)
}

// Profile is the subset of user fields we care about from an
// external IdP. The handler maps these onto our internal `User`.
type Profile struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Username    string `json:"username,omitempty"`
}

// TokenResponse is the result of a successful code exchange.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

// ---------- OAuthStateStore ----------

// OAuthState is a single in-flight OAuth flow. We bind it to (a) a
// random `state` token (CSRF protection), (b) the URL the user
// should land on after success (`redirect`), and (c) the IdP name
// so we know which Provider to use on callback.
type OAuthState struct {
	Token        string
	Provider     string
	Redirect     string
	CreatedAt    time.Time
	Nonce        string // optional, for OIDC
}

// IsExpired returns true if the state is older than 10 minutes.
func (s *OAuthState) IsExpired(now time.Time) bool {
	return now.Sub(s.CreatedAt) > 10*time.Minute
}

// OAuthStateStore persists in-flight OAuth states.
type OAuthStateStore interface {
	Put(s *OAuthState) error
	Get(token string) (*OAuthState, error)
	Consume(token string) (*OAuthState, error)
}

// InMemoryOAuthStateStore is the default implementation. Safe for
// concurrent use. Prunes expired entries every minute.
type InMemoryOAuthStateStore struct {
	mu     sync.RWMutex
	states map[string]*OAuthState
	stopCh chan struct{}
	Now    func() time.Time
}

// NewInMemoryOAuthStateStore creates a state store and starts the
// background pruner. now is the clock source; if nil, time.Now is used.
func NewInMemoryOAuthStateStore(now func() time.Time) *InMemoryOAuthStateStore {
	if now == nil {
		now = time.Now
	}
	s := &InMemoryOAuthStateStore{
		states: make(map[string]*OAuthState),
		stopCh: make(chan struct{}),
		Now:    now,
	}
	go s.pruneLoop()
	return s
}

// Put stores a state. Token must be set.
func (s *InMemoryOAuthStateStore) Put(st *OAuthState) error {
	if st.Token == "" {
		return errors.New("state token required")
	}
	if st.CreatedAt.IsZero() {
		st.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[st.Token] = st
	return nil
}

// Get returns a state by token without consuming it.
func (s *InMemoryOAuthStateStore) Get(token string) (*OAuthState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[token]
	if !ok {
		return nil, ErrOAuthStateInvalid
	}
	if st.IsExpired(s.Now()) {
		return nil, ErrOAuthStateInvalid
	}
	return st, nil
}

// Consume atomically reads and removes a state. This is the right
// call from the callback handler — once consumed, a second click of
// the back button in the browser won't replay the flow.
func (s *InMemoryOAuthStateStore) Consume(token string) (*OAuthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[token]
	if !ok {
		return nil, ErrOAuthStateInvalid
	}
	if st.IsExpired(s.Now()) {
		delete(s.states, token)
		return nil, ErrOAuthStateInvalid
	}
	delete(s.states, token)
	return st, nil
}

// Stop terminates the background pruner. Safe to call multiple times.
func (s *InMemoryOAuthStateStore) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

func (s *InMemoryOAuthStateStore) pruneLoop() {
	t := time.NewTicker(1 * time.Minute)
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

func (s *InMemoryOAuthStateStore) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	for tok, st := range s.states {
		if st.IsExpired(now) {
			delete(s.states, tok)
		}
	}
}

// ---------- Provider Registry ----------

// ProviderRegistry holds the configured OAuth providers. The
// handler reads from this on every /auth/oauth/:name request.
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewProviderRegistry creates an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]Provider)}
}

// Register adds a provider to the registry. Re-registering with the
// same name silently replaces the previous one.
func (r *ProviderRegistry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Get returns a provider by name, or nil if not registered.
func (r *ProviderRegistry) Get(name string) Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[name]
}

// Names returns the names of all registered providers.
func (r *ProviderRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}

// ---------- Built-in Providers ----------

// GoogleProvider is a minimal Google OAuth 2.0 implementation. It
// does NOT use the OIDC discovery endpoint — the well-known
// endpoints are hard-coded. If your tenant is on Google Workspace
// and the discovery URL is different, override the fields below.
type GoogleProvider struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	AuthEndpoint string
	TokenEndpt   string
	ProfileEndpt string
}

// NewGoogleProvider creates a Google provider with default endpoints.
func NewGoogleProvider(clientID, clientSecret string) *GoogleProvider {
	return &GoogleProvider{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"openid", "email", "profile"},
		AuthEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenEndpt:   "https://oauth2.googleapis.com/token",
		ProfileEndpt: "https://www.googleapis.com/oauth2/v3/userinfo",
	}
}

// Name implements Provider.
func (g *GoogleProvider) Name() string { return "google" }

// DisplayName implements Provider.
func (g *GoogleProvider) DisplayName() string { return "Google" }

// AuthURL implements Provider.
func (g *GoogleProvider) AuthURL(state, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", g.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", joinScopes(g.Scopes))
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account")
	return g.AuthEndpoint + "?" + q.Encode()
}

// Exchange implements Provider.
func (g *GoogleProvider) Exchange(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", g.ClientID)
	form.Set("client_secret", g.ClientSecret)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	body, err := httpPostForm(ctx, g.TokenEndpt, form)
	if err != nil {
		return nil, err
	}
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("google: decode token: %w", err)
	}
	return &tr, nil
}

// FetchProfile implements Provider.
func (g *GoogleProvider) FetchProfile(ctx context.Context, accessToken string) (*Profile, error) {
	body, err := httpGetBearer(ctx, g.ProfileEndpt, accessToken)
	if err != nil {
		return nil, err
	}
	var p struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("google: decode profile: %w", err)
	}
	return &Profile{
		ID:          p.Sub,
		Email:       p.Email,
		DisplayName: p.Name,
		AvatarURL:   p.Picture,
		Username:    p.Email,
	}, nil
}

// GitHubProvider is a minimal GitHub OAuth 2.0 implementation. We
// hit /user (returns id, login, name, avatar_url) and /user/emails
// (returns the primary email — GitHub may hide it on /user).
type GitHubProvider struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	AuthEndpoint string
	TokenEndpt   string
	ProfileEndpt string
	EmailsEndpt  string
}

// NewGitHubProvider creates a GitHub provider with default endpoints.
func NewGitHubProvider(clientID, clientSecret string) *GitHubProvider {
	return &GitHubProvider{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"read:user", "user:email"},
		AuthEndpoint: "https://github.com/login/oauth/authorize",
		TokenEndpt:   "https://github.com/login/oauth/access_token",
		ProfileEndpt: "https://api.github.com/user",
		EmailsEndpt:  "https://api.github.com/user/emails",
	}
}

// Name implements Provider.
func (g *GitHubProvider) Name() string { return "github" }

// DisplayName implements Provider.
func (g *GitHubProvider) DisplayName() string { return "GitHub" }

// AuthURL implements Provider.
func (g *GitHubProvider) AuthURL(state, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", g.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", joinScopes(g.Scopes))
	q.Set("state", state)
	q.Set("allow_signup", "true")
	return g.AuthEndpoint + "?" + q.Encode()
}

// Exchange implements Provider.
func (g *GitHubProvider) Exchange(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", g.ClientID)
	form.Set("client_secret", g.ClientSecret)
	form.Set("redirect_uri", redirectURI)
	body, err := httpPostForm(ctx, g.TokenEndpt, form, "application/json", "application/json")
	if err != nil {
		return nil, err
	}
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("github: decode token: %w", err)
	}
	return &tr, nil
}

// FetchProfile implements Provider.
func (g *GitHubProvider) FetchProfile(ctx context.Context, accessToken string) (*Profile, error) {
	body, err := httpGetBearer(ctx, g.ProfileEndpt, accessToken)
	if err != nil {
		return nil, err
	}
	var p struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("github: decode profile: %w", err)
	}
	// GitHub may omit the public email; fall back to /user/emails.
	email := p.Email
	if email == "" {
		if eb, err := httpGetBearer(ctx, g.EmailsEndpt, accessToken); err == nil {
			var emails []struct {
				Email   string `json:"email"`
				Primary bool   `json:"primary"`
			}
			if json.Unmarshal(eb, &emails) == nil {
				for _, e := range emails {
					if e.Primary {
						email = e.Email
						break
					}
				}
			}
		}
	}
	username := p.Login
	if username == "" {
		username = email
	}
	return &Profile{
		ID:          fmt.Sprintf("%d", p.ID),
		Email:       email,
		DisplayName: p.Name,
		AvatarURL:   p.AvatarURL,
		Username:    username,
	}, nil
}

func joinScopes(s []string) string {
	out := ""
	for i, sc := range s {
		if i > 0 {
			out += " "
		}
		out += sc
	}
	return out
}
