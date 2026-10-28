-- +goose Up
CREATE TABLE runtime_tool_inventory (
    revision TEXT PRIMARY KEY REFERENCES runtime_revision(revision) ON DELETE CASCADE,
    data BYTEA NOT NULL
);

-- +goose Down
DROP TABLE runtime_tool_inventory;
