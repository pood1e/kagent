-- +goose Up
CREATE TABLE session_authority (
    session_id UUID PRIMARY KEY REFERENCES session(id) ON DELETE CASCADE,
    initiator_id TEXT NOT NULL CHECK (initiator_id <> ''),
    parent_session_id UUID REFERENCES session_authority(session_id) ON DELETE RESTRICT,
    root_session_id UUID NOT NULL REFERENCES session_authority(session_id) ON DELETE RESTRICT,
    scopes TEXT[] NOT NULL,
    expires_at TIMESTAMPTZ,
    revoked BOOLEAN NOT NULL DEFAULT FALSE,
    CHECK ((parent_session_id IS NULL) = (root_session_id = session_id)),
    CHECK (parent_session_id IS DISTINCT FROM session_id)
);
CREATE INDEX session_authority_parent_idx ON session_authority(parent_session_id);
CREATE INDEX session_authority_initiator_idx ON session_authority(initiator_id);

CREATE TABLE delegation_credential (
    principal_id TEXT PRIMARY KEY CHECK (principal_id <> ''),
    ciphertext BYTEA NOT NULL CHECK (octet_length(ciphertext) > 28),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    refresh_lease UUID,
    refresh_expires_at TIMESTAMPTZ,
    CHECK ((refresh_lease IS NULL) = (refresh_expires_at IS NULL))
);

-- +goose Down
DROP TABLE delegation_credential;
DROP TABLE session_authority;
