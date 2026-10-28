-- +goose Up
ALTER TABLE memory ADD COLUMN team_namespace TEXT NOT NULL DEFAULT '';
ALTER TABLE memory ADD CONSTRAINT memory_team_partition CHECK (
    team_namespace = '' OR (team_namespace ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'
        AND length(team_namespace) <= 63 AND agent_name IS NOT NULL AND user_id IS NOT NULL
        AND agent_name = '' AND user_id = '')
);
CREATE INDEX idx_memory_team ON memory(team_namespace) WHERE team_namespace <> '';

-- +goose Down
DELETE FROM memory WHERE team_namespace <> '';
DROP INDEX idx_memory_team;
ALTER TABLE memory DROP CONSTRAINT memory_team_partition;
ALTER TABLE memory DROP COLUMN team_namespace;
