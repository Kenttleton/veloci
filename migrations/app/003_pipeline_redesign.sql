-- migrations/app/003_pipeline_redesign.sql
-- Pipeline redesign:
--   1. rule_epochs: string terminated_by → user FK (engine never closes epochs)
--   2. review_queue: alert_type (new/drift/ended) + per-component confidence breakdown

-- ── rule_epochs ───────────────────────────────────────────────────────────────

ALTER TABLE rule_epochs DROP COLUMN IF EXISTS terminated_by;
ALTER TABLE rule_epochs ADD COLUMN terminated_by_user_id UUID REFERENCES users(id);

-- ── review_queue ──────────────────────────────────────────────────────────────

ALTER TABLE review_queue
  ADD COLUMN alert_type          TEXT         NOT NULL DEFAULT 'new'
                                 CHECK (alert_type IN ('new', 'drift', 'ended')),
  ADD COLUMN merchant_confidence NUMERIC(4,3),
  ADD COLUMN timing_confidence   NUMERIC(4,3),
  ADD COLUMN amount_confidence   NUMERIC(4,3);

GRANT ALL ON ALL TABLES IN SCHEMA public TO veloci_app_user;
