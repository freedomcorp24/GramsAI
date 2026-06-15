-- +goose Up
-- Precise cost accounting in micro-dollars (millionths of USD) to avoid
-- the lossy whole-cent rounding. 1 cent = 10,000 micros. $1 = 1,000,000 micros.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cost_micros BIGINT NOT NULL DEFAULT 0;

ALTER TABLE users ADD COLUMN IF NOT EXISTS compute_budget_micros BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS compute_used_micros   BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_used_micros     BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_limit_micros    BIGINT NOT NULL DEFAULT 0;

-- Backfill micro budgets from existing cents columns (cents * 10,000 = micros)
UPDATE users SET compute_budget_micros = compute_budget_cents * 10000 WHERE compute_budget_micros = 0;
UPDATE users SET compute_used_micros   = compute_used_cents   * 10000 WHERE compute_used_micros   = 0;
UPDATE users SET daily_limit_micros     = daily_limit_cents    * 10000 WHERE daily_limit_micros    = 0 AND daily_limit_cents > 0;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS daily_limit_micros;
ALTER TABLE users DROP COLUMN IF EXISTS daily_used_micros;
ALTER TABLE users DROP COLUMN IF EXISTS compute_used_micros;
ALTER TABLE users DROP COLUMN IF EXISTS compute_budget_micros;
ALTER TABLE usage_logs DROP COLUMN IF EXISTS cost_micros;
