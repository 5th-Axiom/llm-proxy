-- Add recoverable plaintext storage for issued API keys.
-- Trade-off: the DB file (chmod 0600 by operator convention) must be treated
-- as a first-class secret — a backup or file leak now exposes live tokens.
-- In return, admins can recover a lost key without forcing rotation, which
-- matches how most operators actually want to use the admin UI.
-- Existing pre-migration rows keep token_plaintext NULL; the API surfaces
-- this as "plaintext unavailable" rather than lying with an empty string.
ALTER TABLE api_keys ADD COLUMN token_plaintext TEXT;
