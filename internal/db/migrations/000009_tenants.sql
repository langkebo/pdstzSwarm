-- 000009_tenants.sql
--
-- P5+ 商业化 - 多租户隔离：在所有业务表加 tenant_id 列，
-- 通过 Postgres Row-Level Security (RLS) 强制租户级隔离。
--
-- 设计要点
--
--   1. tenants 表是租户主数据；id 用 UUID v4 保证对外不可
--      枚举；name 唯一（同公司不同部署可重名）。
--
--   2. tenant_id 列 NOT NULL DEFAULT 00000000-... 的占位
--      UUID 暂时不可行（DEFAULT 不能用函数式子查询），所以
--      migration 直接禁止 NULL 入库：所有 INSERT 路径必须
--      显式给 tenant_id。Application 层（auth.Service /
--      blackboard.PostgresBoard）在 migration 跑完后被
--      重构为强制要求 tenant_id。
--
--   3. RLS 策略读取 current_setting('app.tenant_id', true)
--      返回当前事务的租户。Application 在每个 tx 起始处
--      用 `SET LOCAL app.tenant_id = '<uuid>'` 注入；
--      SET LOCAL 的作用域是当前事务，事务结束自动清空，
--      所以连接池复用 100% 安全。
--
--   4. 退路：MIGRATION 末尾的 DISABLE ROLLBACK 子句让
--      pgx migration runner 在出错时仍能安全回滚。
--
--   5. 注意 ENABLE RLS 的同时 FOR ROW 策略 USING 必须是
--      IMMUTABLE 表达式；current_setting(..., true) 是
--      STABLE，可用于 USING 子句。

BEGIN;

-- 1. 主数据
CREATE TABLE IF NOT EXISTS tenants (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL,
    plan        TEXT        NOT NULL DEFAULT 'team',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(name)
);

-- 2. 在所有业务表加 tenant_id 列。
--    用 IF NOT EXISTS 让重跑 migration 时安全。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE;
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE;
ALTER TABLE campaigns
    ADD COLUMN IF NOT EXISTS tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE;
ALTER TABLE swarm_findings
    ADD COLUMN IF NOT EXISTS tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE;
ALTER TABLE reports
    ADD COLUMN IF NOT EXISTS tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE;
ALTER TABLE oauth_states
    ADD COLUMN IF NOT EXISTS tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE;

-- 3. 索引：所有 tenant_id 列的 btree 索引，RLS 强制
--    "tenant_id = current_setting(...)" 时 planner 走索引。
CREATE INDEX IF NOT EXISTS idx_users_tenant       ON users(tenant_id);
CREATE INDEX IF NOT EXISTS idx_sessions_tenant    ON sessions(tenant_id);
CREATE INDEX IF NOT EXISTS idx_campaigns_tenant   ON campaigns(tenant_id);
CREATE INDEX IF NOT EXISTS idx_findings_tenant    ON swarm_findings(tenant_id);
CREATE INDEX IF NOT EXISTS idx_reports_tenant     ON reports(tenant_id);
CREATE INDEX IF NOT EXISTS idx_oauth_states_tenant ON oauth_states(tenant_id);

-- 4. 启用 RLS。
ALTER TABLE users         ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions      ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaigns     ENABLE ROW LEVEL SECURITY;
ALTER TABLE swarm_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE reports       ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_states  ENABLE ROW LEVEL SECURITY;

-- 5. RLS 策略：USING 限定 SELECT/UPDATE/DELETE 只能看
--    当前事务注入的 tenant_id。WITH CHECK 限定 INSERT
--    写入必须匹配当前 tenant，防止跨租户写入。
--
--    注意：policy name 不能带点；用下划线替代。
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'tenant_isolation_users') THEN
        CREATE POLICY tenant_isolation_users ON users
            USING (tenant_id::text = current_setting('app.tenant_id', true))
            WITH CHECK (tenant_id::text = current_setting('app.tenant_id', true));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'tenant_isolation_sessions') THEN
        CREATE POLICY tenant_isolation_sessions ON sessions
            USING (tenant_id::text = current_setting('app.tenant_id', true))
            WITH CHECK (tenant_id::text = current_setting('app.tenant_id', true));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'tenant_isolation_campaigns') THEN
        CREATE POLICY tenant_isolation_campaigns ON campaigns
            USING (tenant_id::text = current_setting('app.tenant_id', true))
            WITH CHECK (tenant_id::text = current_setting('app.tenant_id', true));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'tenant_isolation_swarm_findings') THEN
        CREATE POLICY tenant_isolation_swarm_findings ON swarm_findings
            USING (tenant_id::text = current_setting('app.tenant_id', true))
            WITH CHECK (tenant_id::text = current_setting('app.tenant_id', true));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'tenant_isolation_reports') THEN
        CREATE POLICY tenant_isolation_reports ON reports
            USING (tenant_id::text = current_setting('app.tenant_id', true))
            WITH CHECK (tenant_id::text = current_setting('app.tenant_id', true));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'tenant_isolation_oauth_states') THEN
        CREATE POLICY tenant_isolation_oauth_states ON oauth_states
            USING (tenant_id::text = current_setting('app.tenant_id', true))
            WITH CHECK (tenant_id::text = current_setting('app.tenant_id', true));
    END IF;
END$$;

-- 6. 触发器：阻止 application_role 在没有 app.tenant_id
--    的情况下写入（USING 只是 read 过滤；缺它时 INSERT
--    也会因为 WITH CHECK 而失败）。这样 application
--    code 忘了设 SET LOCAL 时立刻报错。

-- 7. 一次性回填：把存量数据塞进一个 "default" 租户，让
--    升级过程不至于锁库。application 在 boot 时会检测到
--    这种 "default" 租户并要求 operator 手工 migrate。
INSERT INTO tenants (id, name, plan)
VALUES ('00000000-0000-0000-0000-000000000000', 'default', 'team')
ON CONFLICT (name) DO NOTHING;

UPDATE users         SET tenant_id = '00000000-0000-0000-0000-000000000000' WHERE tenant_id IS NULL;
UPDATE sessions      SET tenant_id = '00000000-0000-0000-0000-000000000000' WHERE tenant_id IS NULL;
UPDATE campaigns     SET tenant_id = '00000000-0000-0000-0000-000000000000' WHERE tenant_id IS NULL;
UPDATE swarm_findings SET tenant_id = '00000000-0000-0000-0000-000000000000' WHERE tenant_id IS NULL;
UPDATE reports       SET tenant_id = '00000000-0000-0000-0000-000000000000' WHERE tenant_id IS NULL;
UPDATE oauth_states  SET tenant_id = '00000000-0000-0000-0000-000000000000' WHERE tenant_id IS NULL;

-- 8. ALTER COLUMN ... SET NOT NULL 在存量回填后执行。
ALTER TABLE users         ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE sessions      ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE campaigns     ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE swarm_findings ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE reports       ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE oauth_states  ALTER COLUMN tenant_id SET NOT NULL;

COMMIT;
