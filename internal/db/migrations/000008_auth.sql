-- 000008_auth.sql
--
-- P5+ 鉴权闭环：users / sessions / oauth_states 三张表。
--
-- 设计要点：
--   1. **users 是源**：username / email 唯一索引；password_hash
--      可空（OAuth 用户没有密码）；provider + provider_id 联合唯一
--      索引支持 OAuth 用户的可重复登录。
--   2. **sessions 是缓存**：token 主键，user_id 外键，expires_at
--      索引。session 表是可清理的（过期批量删除），不影响主流程。
--   3. **oauth_states 是临时表**：state 一次性消费，10 分钟 TTL；
--      没必要持久化（防 CSRF 设计的强约束反而是它的优点）。
--   4. **与 reports 表一致的 ON DELETE CASCADE**：删除用户 → 自动
--      清掉该用户的所有 session。
--   5. **Postgres 扩展**：gen_random_uuid() 来自 pgcrypto 扩展。
--      如果未启用则回退到应用层生成（RandomID）。

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------- users ----------

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT NOT NULL,
    email         TEXT,
    display_name  TEXT,
    avatar_url    TEXT,
    role          TEXT NOT NULL DEFAULT 'operator'
                  CHECK (role IN ('admin', 'operator', 'viewer')),
    provider      TEXT NOT NULL DEFAULT 'password',
    provider_id   TEXT,
    password_hash TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 唯一索引：username 大小写不敏感
CREATE UNIQUE INDEX IF NOT EXISTS users_username_lower_idx
    ON users (LOWER(username));

-- 唯一索引：email 非空时大小写不敏感
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_idx
    ON users (LOWER(email))
    WHERE email IS NOT NULL;

-- 唯一索引：provider + provider_id 联合唯一（OAuth 用户可重复登录）
CREATE UNIQUE INDEX IF NOT EXISTS users_provider_idempotent_idx
    ON users (provider, provider_id)
    WHERE provider_id IS NOT NULL;

-- role 索引：管理员查询常用
CREATE INDEX IF NOT EXISTS users_role_idx ON users (role);

-- ---------- sessions ----------

CREATE TABLE IF NOT EXISTS sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    username    TEXT NOT NULL,
    role        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    user_agent  TEXT,
    ip          INET
);

CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions (user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions (expires_at);

-- ---------- oauth_states ----------

CREATE TABLE IF NOT EXISTS oauth_states (
    token       UUID PRIMARY KEY,
    provider    TEXT NOT NULL,
    redirect    TEXT NOT NULL DEFAULT '/',
    nonce       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS oauth_states_created_at_idx ON oauth_states (created_at);

COMMIT;
