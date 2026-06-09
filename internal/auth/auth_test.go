package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// ---- helpers ----

func newTestService() *Service {
	s := NewService()
	fixed := func() time.Time { return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC) }
	s.Now = fixed
	// Inject the fixed clock into the stores so IsExpired checks
	// use the test clock too.
	iss := s.Sessions.(*InMemorySessionStore)
	iss.Now = fixed
	oss := s.OAuthStates.(*InMemoryOAuthStateStore)
	oss.Now = fixed
	return s
}

func newTestApp(svc *Service) *fiber.App {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})
	h := NewHandler(svc, "http://localhost:8080", false)
	api := app.Group("/api/v1")
	api.Use(h.SessionMiddleware())
	h.Register(api)
	return app
}

func doJSON(t *testing.T, app *fiber.App, method, path, body string, cookies ...string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.Header.Set("Cookie", c)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func extractSessionCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatalf("no session cookie set")
	return ""
}

func TestHashPassword_StableAndUnique(t *testing.T) {
	a := HashPassword("hunter2", "user-1")
	b := HashPassword("hunter2", "user-1")
	c := HashPassword("hunter2", "user-2")
	if a != b {
		t.Errorf("same input should hash to the same value")
	}
	if a == c {
		t.Errorf("different salts should hash differently")
	}
}

func TestVerifyPassword(t *testing.T) {
	s := newTestService()
	u, err := s.Users.GetByUsername("operator")
	if err != nil {
		t.Fatalf("seed user missing: %v", err)
	}
	if err := VerifyPassword(u, "operator"); err != nil {
		t.Errorf("expected verify(operator, operator) to succeed: %v", err)
	}
	if err := VerifyPassword(u, "wrong"); err == nil {
		t.Errorf("expected verify(operator, wrong) to fail")
	}
	if err := VerifyPassword(nil, "anything"); err == nil {
		t.Errorf("expected verify(nil, anything) to fail")
	}
}

func TestService_LoginWithPassword(t *testing.T) {
	s := newTestService()
	sess, u, err := s.LoginWithPassword("operator", "operator", false)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if u.Username != "operator" {
		t.Errorf("wrong user: %v", u.Username)
	}
	if sess.ID == "" {
		t.Errorf("session id empty")
	}
	if sess.ExpiresAt.Sub(sess.CreatedAt) != DefaultSessionTTL {
		t.Errorf("default TTL not applied")
	}
}

func TestService_LoginWithPassword_Remember(t *testing.T) {
	s := newTestService()
	sess, _, err := s.LoginWithPassword("operator", "operator", true)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if sess.ExpiresAt.Sub(sess.CreatedAt) != RememberedSessionTTL {
		t.Errorf("remembered TTL not applied")
	}
}

func TestService_LoginWithPassword_RejectsBadCreds(t *testing.T) {
	s := newTestService()
	if _, _, err := s.LoginWithPassword("operator", "WRONG", false); err == nil {
		t.Errorf("expected bad password to fail")
	}
	if _, _, err := s.LoginWithPassword("nobody", "operator", false); err == nil {
		t.Errorf("expected missing user to fail")
	}
}

func TestService_AuthenticateSession(t *testing.T) {
	s := newTestService()
	sess, _, err := s.LoginWithPassword("operator", "operator", false)
	if err != nil {
		t.Fatal(err)
	}
	u, sess2, err := s.AuthenticateSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "operator" {
		t.Errorf("wrong user returned: %v", u.Username)
	}
	if sess2.ID != sess.ID {
		t.Errorf("session id mismatch")
	}
}

func TestService_AuthenticateSession_Expired(t *testing.T) {
	s := newTestService()
	sess, _, err := s.LoginWithPassword("operator", "operator", false)
	if err != nil {
		t.Fatal(err)
	}
	// Force expiry by mutating the in-memory store. Use the
	// injected clock so the expiry check is consistent.
	iss := s.Sessions.(*InMemorySessionStore)
	iss.mu.Lock()
	iss.sessions[sess.ID].ExpiresAt = s.Now().Add(-1 * time.Minute)
	iss.mu.Unlock()
	if _, _, err := s.AuthenticateSession(sess.ID); err == nil {
		t.Errorf("expected expired session to fail")
	}
}

func TestService_LogoutAndLogoutEverywhere(t *testing.T) {
	s := newTestService()
	sess, u, err := s.LoginWithPassword("operator", "operator", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(sess.ID); err != nil {
		t.Errorf("logout: %v", err)
	}
	if _, _, err := s.AuthenticateSession(sess.ID); err == nil {
		t.Errorf("expected session to be gone after logout")
	}
	// Open two more, kill all
	for i := 0; i < 2; i++ {
		_, _, err := s.LoginWithPassword("operator", "operator", false)
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.LogoutEverywhere(u.ID)
	if err != nil || n != 2 {
		t.Errorf("expected 2 sessions removed, got n=%d err=%v", n, err)
	}
}

// ---- OAuth state machine ----

func TestOAuthState_PutGetConsume(t *testing.T) {
	s := newTestService()
	st := &OAuthState{
		Token:     "tok-1",
		Provider:  "google",
		Redirect:  "/campaigns",
		CreatedAt: s.Now(),
	}
	if err := s.OAuthStates.Put(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.OAuthStates.Get("tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "google" {
		t.Errorf("wrong provider")
	}
	// Consume: subsequent Get should fail
	if _, err := s.OAuthStates.Consume("tok-1"); err != nil {
		t.Errorf("consume: %v", err)
	}
	if _, err := s.OAuthStates.Get("tok-1"); err == nil {
		t.Errorf("expected get to fail after consume")
	}
}

func TestOAuthState_Expired(t *testing.T) {
	s := newTestService()
	// Override the pruner's perception of now by inserting a stale state.
	iss := s.OAuthStates.(*InMemoryOAuthStateStore)
	iss.states["stale"] = &OAuthState{
		Token:     "stale",
		Provider:  "google",
		CreatedAt: s.Now().Add(-15 * time.Minute),
	}
	if _, err := s.OAuthStates.Consume("stale"); err == nil {
		t.Errorf("expected expired state to fail")
	}
}

// ---- Fake provider for the integration test ----

type fakeProvider struct {
	profile   Profile
	calls     int
	authCount int
}

func (f *fakeProvider) Name() string           { return "fake" }
func (f *fakeProvider) DisplayName() string    { return "Fake" }
func (f *fakeProvider) AuthURL(s, _ string) string {
	f.calls++
	return "https://idp.example/authorize?state=" + s
}
func (f *fakeProvider) Exchange(_ context.Context, _, _ string) (*TokenResponse, error) {
	return &TokenResponse{AccessToken: "tok"}, nil
}
func (f *fakeProvider) FetchProfile(_ context.Context, _ string) (*Profile, error) {
	return &f.profile, nil
}

func TestOAuth_EndToEnd(t *testing.T) {
	s := newTestService()
	s.Providers.Register(&fakeProvider{profile: Profile{
		ID:          "12345",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		Username:    "alice",
	}})
	tok, _, err := s.BeginOAuth("fake", "/campaigns")
	if err != nil {
		t.Fatal(err)
	}
	url, err := s.BuildAuthURL("fake", tok, "https://app.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "state="+tok) {
		t.Errorf("auth URL missing state: %s", url)
	}
	sess, u, err := s.CompleteOAuth(context.Background(), tok, "auth-code-xyz", "https://app.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "alice" {
		t.Errorf("expected alice, got %s", u.Username)
	}
	if sess.ID == "" {
		t.Errorf("empty session id")
	}
	// Second call should re-use the same user (provider+id stable).
	// We need a fresh state token since CompleteOAuth consumed the
	// first one.
	tok2, _, err := s.BeginOAuth("fake", "/campaigns")
	if err != nil {
		t.Fatal(err)
	}
	sess2, u2, err := s.CompleteOAuth(context.Background(), tok2, "auth-code", "https://app.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess2
	if u2.ID != u.ID {
		t.Errorf("expected same user on second login, got %s vs %s", u.ID, u2.ID)
	}
}

// ---- HTTP handler ----

func TestHandler_LoginMeLogout(t *testing.T) {
	svc := newTestService()
	app := newTestApp(svc)

	// 1. login with bad creds → 401
	resp, _ := doJSON(t, app, "POST", "/api/v1/auth/login",
		`{"username":"operator","password":"WRONG"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// 2. login with good creds → 200 + cookie
	resp, _ = doJSON(t, app, "POST", "/api/v1/auth/login",
		`{"username":"operator","password":"operator"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	cookie := extractSessionCookie(t, resp)

	// 3. /auth/me with cookie → 200 user
	resp, body := doJSON(t, app, "GET", "/api/v1/auth/me", "", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	var meBody struct {
		User User `json:"user"`
	}
	if err := json.Unmarshal(body, &meBody); err != nil {
		t.Fatal(err)
	}
	if meBody.User.Username != "operator" {
		t.Errorf("wrong user: %s", meBody.User.Username)
	}

	// 4. /auth/me without cookie → 401
	resp, _ = doJSON(t, app, "GET", "/api/v1/auth/me", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for guest, got %d", resp.StatusCode)
	}

	// 5. logout → 200 + cookie cleared
	resp, _ = doJSON(t, app, "POST", "/api/v1/auth/logout", "", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("expected session cookie to be cleared on logout")
	}
}

func TestHandler_Providers(t *testing.T) {
	svc := newTestService()
	svc.Providers.Register(&fakeProvider{profile: Profile{ID: "1"}})
	app := newTestApp(svc)
	resp, body := doJSON(t, app, "GET", "/api/v1/auth/providers", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var parsed struct {
		Providers []struct {
			Name  string `json:"name"`
			Label string `json:"label"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Providers) == 0 {
		t.Errorf("expected at least one provider")
	}
}

func TestHandler_RequireAuth(t *testing.T) {
	svc := newTestService()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	h := NewHandler(svc, "http://localhost:8080", false)
	app.Use("/api/v1", h.SessionMiddleware())
	app.Get("/api/v1/protected", h.RequireAuth(), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	// No cookie → 401
	resp, _ := doJSON(t, app, "GET", "/api/v1/protected", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for guest, got %d", resp.StatusCode)
	}

	// Login + cookie → 200
	login := newTestApp(svc)
	resp, _ = doJSON(t, login, "POST", "/api/v1/auth/login",
		`{"username":"operator","password":"operator"}`)
	cookie := extractSessionCookie(t, resp)
	resp, body := doJSON(t, app, "GET", "/api/v1/protected", "", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%s)", resp.StatusCode, string(body))
	}
}

// Ensure we don't accidentally regress the bytes import (some
// helpers in earlier drafts used bytes.Buffer).
var _ = bytes.NewBuffer

