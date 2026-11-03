-- +goose Up
ALTER TABLE agent_revision
 ADD COLUMN dependencies BYTEA,
 DROP COLUMN mcp_tools,
 DROP COLUMN models;
ALTER TABLE session_authority ADD COLUMN resource_grants BYTEA;

-- +goose Down
ALTER TABLE session_authority DROP COLUMN resource_grants;
ALTER TABLE agent_revision
 DROP COLUMN dependencies,
 ADD COLUMN mcp_tools JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(mcp_tools) = 'array'),
 ADD COLUMN models JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(models) = 'array');
