-- +goose Up
CREATE VIEW task_catalog_session AS
SELECT i.id AS session_id, i.user_id AS creator, i.state AS runtime_state,
    i.operation, i.prepared_revision, i.data AS session_data,
    s.name AS session_name, s.updated_at AS session_updated_at,
    s.history_id, r.namespace, r.agent_name,
    COALESCE(NULLIF(a.initiator_id, ''), i.user_id) AS initiator_id,
    COALESCE(a.root_session_id, i.id) AS root_session_id, a.expires_at
FROM session_record i
JOIN session s ON s.id = i.id
JOIN agent_runtime_revision r ON r.revision = i.prepared_revision
LEFT JOIN session_authority a ON a.session_id = i.id
WHERE i.state <> 'RUNTIME_STATE_DELETED';

CREATE VIEW task_catalog_task AS
SELECT s.*, t.id AS task_id, t.state, t.status_timestamp, t.created_at,
    t.initial_prompt, t.pending_tool_approval, t.pending_user_input, t.tool_call_count,
    t.data AS task_data, e.parent_task_id, p.id AS parent_session_id,
    e.root_task_id, root_session.id AS execution_root_session_id,
    e.relation, e.background, e.resumed_task_id, resumed.id AS resumed_session_id
FROM task_catalog_session s
JOIN session_task t ON t.history_id = s.history_id
LEFT JOIN task_execution e ON e.task_id = t.id
LEFT JOIN task_execution parent ON parent.task_id = e.parent_task_id
LEFT JOIN session p ON p.history_id = parent.history_id
LEFT JOIN task_execution root ON root.task_id = e.root_task_id
LEFT JOIN session root_session ON root_session.history_id = root.history_id
LEFT JOIN task_execution resumed_task ON resumed_task.task_id = e.resumed_task_id
LEFT JOIN session resumed ON resumed.history_id = resumed_task.history_id;

-- +goose StatementBegin
CREATE FUNCTION catalog_sessions(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[])
RETURNS SETOF task_catalog_session LANGUAGE SQL STABLE AS $$
    SELECT s.* FROM task_catalog_session s
    WHERE s.namespace = ns AND (owner_id = '' OR s.creator = owner_id)
      AND (other_id = '' OR s.initiator_id <> other_id)
      AND (tree_root IS NULL OR s.root_session_id = tree_root)
      AND (allowed_ids IS NULL OR s.session_id = ANY(allowed_ids))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION catalog_tasks(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[])
RETURNS SETOF task_catalog_task LANGUAGE SQL STABLE AS $$
    SELECT t.* FROM task_catalog_task t
    WHERE t.namespace = ns AND (owner_id = '' OR t.creator = owner_id)
      AND (other_id = '' OR t.initiator_id <> other_id)
      AND (tree_root IS NULL OR t.root_session_id = tree_root)
      AND (allowed_ids IS NULL OR t.session_id = ANY(allowed_ids))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION catalog_active_only(filters JSONB) RETURNS BOOLEAN LANGUAGE SQL IMMUTABLE AS $$
    SELECT jsonb_array_length(filters)>0 AND COALESCE(bool_and(
        COALESCE(f.pending_tool_approval,FALSE) OR COALESCE(f.state IN (
            'TASK_STATE_SUBMITTED','TASK_STATE_WORKING','TASK_STATE_INPUT_REQUIRED',
            'TASK_STATE_AUTH_REQUIRED','TASK_STATE_UNSPECIFIED'),FALSE)),FALSE)
    FROM jsonb_to_recordset(filters) AS f(state TEXT,pending_tool_approval BOOLEAN)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION catalog_record_candidates(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[], filter_agent TEXT, filters JSONB)
RETURNS SETOF task_catalog_task LANGUAGE SQL STABLE AS $$
WITH mode AS MATERIALIZED (SELECT catalog_active_only(filters) AS active_only), candidates AS (
    SELECT * FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids) WHERE NOT (SELECT active_only FROM mode)
    UNION ALL
    SELECT * FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids)
    WHERE (SELECT active_only FROM mode)
      AND state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED')
)
SELECT t.* FROM candidates t
WHERE (filter_agent='' OR t.namespace||'/'||t.agent_name=filter_agent)
  AND (jsonb_array_length(filters)=0 OR EXISTS(
      SELECT 1 FROM jsonb_to_recordset(filters) AS f(state TEXT,pending_tool_approval BOOLEAN)
      WHERE (f.state IS NULL OR f.state=t.state)
        AND (f.pending_tool_approval IS NULL OR f.pending_tool_approval=t.pending_tool_approval)))
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION catalog_record_candidates(TEXT,TEXT,TEXT,UUID,UUID[],TEXT,JSONB);
DROP FUNCTION catalog_active_only(JSONB);
DROP FUNCTION catalog_tasks(TEXT, TEXT, TEXT, UUID, UUID[]);
DROP FUNCTION catalog_sessions(TEXT, TEXT, TEXT, UUID, UUID[]);
DROP VIEW task_catalog_task;
DROP VIEW task_catalog_session;
