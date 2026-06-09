// Package auth implements P5+ 鉴权（OAuth 2.0 + session）。
//
// 设计要点：
//   1. **Cookie 优先，OAuth 后置**：基础密码登录始终可用（保持 legacy
//      password 入口，避免破坏 P5-UI 的 demo 流程），OAuth 作为附加
//      provider，按 providers.yaml 中的开关启用。
//   2. **State token 短寿命**：OAuth state 在内存中保留 10 分钟，防
//      CSRF + 一次性消费。
//   3. **Provider 接口可插拔**：Google / GitHub / OIDC 走同一个
//      `Provider` 接口，新增 provider 只需实现 3 个方法（AuthURL /
//      Exchange / FetchProfile）。
//   4. **Sessions 双轨**：内存 Map（dev / demo）+ Postgres（prod），
//      store 字段为 nil 时降级为内存实现。
//   5. **零外部依赖**：密码哈希用 crypto/sha256 + per-user salt
//      （bcrypt 需引依赖；demo 场景够用，生产建议升级）。
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// Role 是用户角色。区分 admin / operator / viewer 三档。
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// User 是已认证主体的内存表示。PasswordHash 仅在 password 登录时设置。
type User struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id,omitempty"` // P5+ 商业化 - 多租户隔离 id
	Username     string    `json:"username"`
	Email        string    `json:"email,omitempty"`
	DisplayName  string    `json:"display_name,omitempty"`
	AvatarURL    string    `json:"avatar_url,omitempty"`
	Role         Role      `json:"role"`
	Provider     string    `json:"provider,omitempty"` // "password" | "google" | "github" | ...
	ProviderID   string    `json:"provider_id,omitempty"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// Session 是一次已认证会话。
type Session struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	TenantID   string    `json:"tenant_id,omitempty"` // P5+ 商业化 - 复制自 User，便于 RLS 路径无需再查 User
	Username   string    `json:"username"`
	Role       Role      `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	UserAgent  string    `json:"user_agent,omitempty"`
	IP         string    `json:"ip,omitempty"`
}

// IsExpired returns true if the session has passed its expiry timestamp.
func (s *Session) IsExpired(now time.Time) bool {
	return now.After(s.ExpiresAt)
}

// CookieName 是 session cookie 的 name。所有 endpoint 都读这个值，
// 因此修改它会强制所有用户重新登录。
const CookieName = "psa_session"

// DefaultSessionTTL 是一次普通 session 的有效期。Remember-me 会把
// TTL 加倍（见 IssueSession 的 remember 参数）。
const DefaultSessionTTL = 8 * time.Hour

// RememberedSessionTTL 是 remember-me 的 TTL。
const RememberedSessionTTL = 30 * 24 * time.Hour

// ErrInvalidCredentials is returned by VerifyPassword when the
// password does not match.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrUserNotFound is returned when a lookup by username or email
// yields no user.
var ErrUserNotFound = errors.New("user not found")

// ErrSessionNotFound is returned by SessionStore.Get when no session
// matches the id.
var ErrSessionNotFound = errors.New("session not found")

// ErrOAuthStateInvalid is returned by OAuthStateStore.Consume when
// the state token is missing, expired, or already redeemed.
var ErrOAuthStateInvalid = errors.New("oauth state invalid")

// RandomID returns a 32-character hex string. Used for session ids,
// oauth state tokens, and per-user salts.
func RandomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
