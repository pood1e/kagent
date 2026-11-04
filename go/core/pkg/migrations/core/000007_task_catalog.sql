-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE session
    ADD COLUMN name TEXT NOT NULL DEFAULT '',
    ADD COLUMN updated_at TIMESTAMPTZ;

ALTER TABLE session_task
    ADD COLUMN initial_prompt TEXT NOT NULL DEFAULT '',
    ADD COLUMN pending_tool_approval BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN pending_user_input BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN tool_call_count BIGINT NOT NULL DEFAULT 0 CHECK (tool_call_count >= 0);

-- +goose StatementBegin
DO $$
DECLARE extension_schema TEXT;
BEGIN
    SELECT n.nspname INTO STRICT extension_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_trgm';
    EXECUTE format('CREATE INDEX session_catalog_name_idx ON session USING GIN (lower(name) %I.gin_trgm_ops)',extension_schema);
    EXECUTE format('CREATE INDEX session_task_catalog_prompt_idx ON session_task USING GIN (lower(initial_prompt) %I.gin_trgm_ops)',extension_schema);
END $$;
-- +goose StatementEnd
CREATE INDEX session_task_catalog_activity_idx
    ON session_task (state, (COALESCE(status_timestamp, created_at)) DESC, id);

CREATE INDEX session_task_catalog_active_idx ON session_task (id, history_id)
    WHERE state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED');

-- +goose Down
DROP INDEX session_task_catalog_active_idx;
DROP INDEX session_task_catalog_activity_idx;
DROP INDEX session_task_catalog_prompt_idx;
DROP INDEX session_catalog_name_idx;
ALTER TABLE session_task DROP COLUMN tool_call_count, DROP COLUMN pending_user_input, DROP COLUMN pending_tool_approval, DROP COLUMN initial_prompt;
ALTER TABLE session DROP COLUMN updated_at, DROP COLUMN name;
