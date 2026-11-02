-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_sessions(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[])
RETURNS SETOF task_catalog_session LANGUAGE SQL STABLE AS $$
    SELECT s.* FROM task_catalog_session s
    JOIN session_authority a ON a.session_id = s.session_id
    WHERE s.namespace = ns
      AND a.workspace_uid = NULLIF(current_setting('kagent.catalog_workspace_uid', TRUE), '')
      AND (owner_id = '' OR s.creator = owner_id)
      AND (other_id = '' OR s.initiator_id <> other_id)
      AND (tree_root IS NULL OR s.root_session_id = tree_root)
      AND (allowed_ids IS NULL OR s.session_id = ANY(allowed_ids))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_tasks(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[])
RETURNS SETOF task_catalog_task LANGUAGE SQL STABLE AS $$
    SELECT t.* FROM task_catalog_task t
    JOIN session_authority a ON a.session_id = t.session_id
    WHERE t.namespace = ns
      AND a.workspace_uid = NULLIF(current_setting('kagent.catalog_workspace_uid', TRUE), '')
      AND (owner_id = '' OR t.creator = owner_id)
      AND (other_id = '' OR t.initiator_id <> other_id)
      AND (tree_root IS NULL OR t.root_session_id = tree_root)
      AND (allowed_ids IS NULL OR t.session_id = ANY(allowed_ids))
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_sessions(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[])
RETURNS SETOF task_catalog_session LANGUAGE SQL STABLE AS $$
    SELECT s.* FROM task_catalog_session s
    WHERE s.namespace = ns AND (owner_id = '' OR s.creator = owner_id)
      AND (other_id = '' OR s.initiator_id <> other_id)
      AND (tree_root IS NULL OR s.root_session_id = tree_root)
      AND (allowed_ids IS NULL OR s.session_id = ANY(allowed_ids))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_tasks(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[])
RETURNS SETOF task_catalog_task LANGUAGE SQL STABLE AS $$
    SELECT t.* FROM task_catalog_task t
    WHERE t.namespace = ns AND (owner_id = '' OR t.creator = owner_id)
      AND (other_id = '' OR t.initiator_id <> other_id)
      AND (tree_root IS NULL OR t.root_session_id = tree_root)
      AND (allowed_ids IS NULL OR t.session_id = ANY(allowed_ids))
$$;
-- +goose StatementEnd
