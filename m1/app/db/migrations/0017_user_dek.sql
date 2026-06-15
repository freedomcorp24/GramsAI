-- +goose Up
-- Per-user wrapped DEK for encrypted chats + memory summaries.
-- enc_dek  = the user's random DEK, AES-GCM-wrapped by a KEK derived (Argon2id)
--            from their login password. Unwrapped at login, pushed to Redis for
--            the session. The server never stores the password or the raw DEK.
-- dek_salt = Argon2id salt for deriving the password-KEK.
-- Both NULL until the account's DEK is provisioned (existing accounts get one
-- lazily on next login; new accounts at signup).
ALTER TABLE users ADD COLUMN IF NOT EXISTS enc_dek  BYTEA;
ALTER TABLE users ADD COLUMN IF NOT EXISTS dek_salt BYTEA;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS dek_salt;
ALTER TABLE users DROP COLUMN IF EXISTS enc_dek;
