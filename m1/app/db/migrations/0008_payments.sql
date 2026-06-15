-- +goose Up
-- Payment history + idempotency for the provider-agnostic payment layer.
CREATE TABLE IF NOT EXISTS payments (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL,                 -- 'nowpayments', 'btcpay', ...
    provider_pid    TEXT,                          -- provider's payment id (idempotency)
    kind            TEXT NOT NULL,                 -- 'subscription' | 'topup'
    tier            TEXT,                          -- for subscriptions
    period          TEXT,                          -- 'monthly' | 'yearly' (subscriptions)
    days            INT NOT NULL DEFAULT 0,        -- access days granted
    budget_micros   BIGINT NOT NULL DEFAULT 0,     -- compute added (topups)
    price_usd_cents BIGINT NOT NULL DEFAULT 0,     -- what we charged
    pay_currency    TEXT,                          -- coin actually paid (xmr, usdttrc20, btc...)
    status          TEXT NOT NULL DEFAULT 'waiting', -- waiting|confirming|finished|failed|expired
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS payments_provider_pid_uniq ON payments(provider, provider_pid) WHERE provider_pid IS NOT NULL;
CREATE INDEX IF NOT EXISTS payments_user_idx ON payments(user_id);

-- Monthly compute-budget reset cycle. Access (paid_until) is separate from the
-- monthly usage allowance, which refills every 30 days even on yearly plans.
ALTER TABLE users ADD COLUMN IF NOT EXISTS budget_reset TIMESTAMPTZ;
-- monthly_budget_micros = the tier's monthly compute allotment (refill target).
-- Seeded from tier_defaults on subscription; compute_budget_micros is the live
-- remaining balance for the current cycle.
ALTER TABLE users ADD COLUMN IF NOT EXISTS monthly_budget_micros BIGINT NOT NULL DEFAULT 0;

-- +goose Down
DROP TABLE IF EXISTS payments;
ALTER TABLE users DROP COLUMN IF EXISTS budget_reset;
ALTER TABLE users DROP COLUMN IF EXISTS monthly_budget_micros;
