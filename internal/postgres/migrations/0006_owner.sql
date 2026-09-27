-- +goose Up
-- Multi-tenant ownership (Telegram bot, issue #100 arc): watches and
-- orders created by the bot carry owner='tg:<chat_id>'; rows created by
-- fleet callers keep owner=''. Scoping is caller-asserted — the bearer
-- secret is the auth boundary, the bot is the trusted caller that always
-- stamps its user's owner. Queries filter when owner is non-empty;
-- owner='' callers stay unscoped (backward compat for fleet MCP/REST).
ALTER TABLE watches ADD COLUMN owner text NOT NULL DEFAULT '';
ALTER TABLE orders  ADD COLUMN owner text NOT NULL DEFAULT '';
CREATE INDEX watches_owner_idx ON watches (owner) WHERE owner <> '';
CREATE INDEX orders_owner_idx  ON orders  (owner) WHERE owner <> '';

-- +goose Down
ALTER TABLE watches DROP COLUMN owner;
ALTER TABLE orders  DROP COLUMN owner;
