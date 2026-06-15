-- +goose Up
-- +goose StatementBegin
INSERT INTO tier_defaults (tier, budget_micros, daily_limit_micros) VALUES
  ('basic',  10000000, 0),
  ('pro',    25000000, 0),
  ('max',    60000000, 0),
  ('ultra', 130000000, 0)
ON CONFLICT (tier) DO UPDATE SET
  budget_micros      = EXCLUDED.budget_micros,
  daily_limit_micros = EXCLUDED.daily_limit_micros;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM tier_defaults WHERE tier IN ('basic','pro','max','ultra');
-- +goose StatementEnd
