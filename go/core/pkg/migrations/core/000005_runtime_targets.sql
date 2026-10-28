-- +goose Up
ALTER TABLE agent_revision
 ADD COLUMN mcp_tools JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(mcp_tools) = 'array'),
 ADD COLUMN models JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(models) = 'array');

-- +goose Down
ALTER TABLE agent_revision DROP COLUMN models, DROP COLUMN mcp_tools;
