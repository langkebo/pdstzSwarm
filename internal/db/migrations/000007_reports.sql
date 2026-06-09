-- 000007_reports.sql: Penetration test report storage
--
-- A report is an immutable, point-in-time rendering of a campaign's
-- findings into Markdown. Reports are append-only: regenerating a
-- campaign's report produces a new row with a new id; previous reports
-- are kept for audit / diff purposes.
--
-- The Markdown body is the human-readable form; the `summary` and
-- `sections` JSONB columns let the front-end render the same content
-- as structured cards without re-parsing Markdown.

CREATE TABLE IF NOT EXISTS reports (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    campaign_id     UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'ready',  -- queued | generating | ready | failed
    format          TEXT NOT NULL DEFAULT 'markdown', -- markdown | json
    markdown        TEXT NOT NULL DEFAULT '',
    summary         JSONB NOT NULL DEFAULT '{}'::jsonb,
    sections        JSONB NOT NULL DEFAULT '[]'::jsonb,
    byte_size       INTEGER NOT NULL DEFAULT 0,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_reports_campaign
    ON reports(campaign_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_reports_created
    ON reports(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_reports_status
    ON reports(status) WHERE status <> 'ready';
