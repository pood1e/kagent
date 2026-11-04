-- +goose Up
ALTER TABLE session_authority ADD COLUMN workspace_uid TEXT NOT NULL DEFAULT '';

CREATE TABLE authorization_change (
    request_id UUID PRIMARY KEY,
    actor_id TEXT NOT NULL CHECK (actor_id <> ''),
    store_id TEXT NOT NULL CHECK (store_id <> ''),
    model_id TEXT NOT NULL CHECK (model_id <> ''),
    binding BYTEA NOT NULL CHECK (octet_length(binding) > 0),
    grant_access BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    applied_at TIMESTAMPTZ
);

-- +goose Down
DROP TABLE authorization_change;
ALTER TABLE session_authority DROP COLUMN workspace_uid;
