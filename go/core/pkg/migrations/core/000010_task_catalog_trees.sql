-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION catalog_tree_activity(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[], anchors TEXT[])
RETURNS TABLE(anchor_id TEXT, state TEXT, pending_tool_approval BOOLEAN, updated_at TIMESTAMPTZ, member_ids TEXT[])
LANGUAGE SQL STABLE AS $$
WITH RECURSIVE visible AS MATERIALIZED (
    SELECT task_id, session_id, namespace, agent_name, state, status_timestamp,
        parent_task_id, background, pending_tool_approval, pending_user_input
    FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids)
    WHERE root_task_id IN (SELECT e.root_task_id FROM task_execution e JOIN unnest(anchors) a(task_id) ON a.task_id=e.task_id)
), walk AS (
    SELECT v.task_id AS anchor_id, v.task_id, NOT COALESCE(v.background,FALSE) AS foreground,
        ARRAY[v.task_id] AS path
    FROM visible v WHERE v.task_id IN (SELECT unnest(anchors))
    UNION ALL
    SELECT w.anchor_id,c.task_id,w.foreground AND NOT COALESCE(c.background,FALSE),w.path||c.task_id
    FROM walk w JOIN visible c ON c.parent_task_id=w.task_id
    WHERE NOT c.task_id=ANY(w.path)
), latest AS (
    SELECT DISTINCT ON(w.anchor_id) w.anchor_id,v.state,v.status_timestamp
    FROM walk w JOIN visible v ON v.task_id=w.task_id JOIN visible r ON r.task_id=w.anchor_id
    WHERE v.session_id=r.session_id AND v.namespace=r.namespace AND v.agent_name=r.agent_name
    ORDER BY w.anchor_id,v.status_timestamp DESC NULLS LAST,w.path
), running AS (
    SELECT DISTINCT ON(w.anchor_id) w.anchor_id,v.state
    FROM walk w JOIN visible v ON v.task_id=w.task_id
    WHERE w.foreground AND v.state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED')
    ORDER BY w.anchor_id,w.path
), facts AS (
    SELECT w.anchor_id,
        bool_or(w.foreground AND v.pending_tool_approval) AS approval,
        bool_or(w.foreground AND v.pending_user_input) AS question,
        bool_or(w.foreground AND v.state='TASK_STATE_CANCELED'
            AND COALESCE(v.status_timestamp,'epoch'::timestamptz)>=COALESCE(l.status_timestamp,'epoch'::timestamptz)) AS canceled,
        max(v.status_timestamp) AS updated_at,array_agg(v.task_id ORDER BY w.path) AS member_ids
    FROM walk w JOIN visible v ON v.task_id=w.task_id JOIN latest l USING(anchor_id)
    GROUP BY w.anchor_id
)
SELECT f.anchor_id,
    CASE WHEN f.approval OR f.question THEN 'TASK_STATE_INPUT_REQUIRED'
         WHEN r.state IS NOT NULL THEN r.state
         WHEN f.canceled THEN 'TASK_STATE_CANCELED' ELSE l.state END,
    f.approval,f.updated_at,f.member_ids
FROM facts f JOIN latest l USING(anchor_id) LEFT JOIN running r USING(anchor_id)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION catalog_task_groups(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[], boundaries TEXT[], filter_agent TEXT, needle TEXT, filters JSONB)
RETURNS TABLE(anchor_id TEXT, anchor_session_id UUID, initiator_id TEXT, initiator_session_id UUID,
    state TEXT, pending_tool_approval BOOLEAN, updated_at TIMESTAMPTZ, member_ids TEXT[])
LANGUAGE SQL STABLE AS $$
WITH RECURSIVE mode AS MATERIALIZED (
    SELECT catalog_active_only(filters) AS active_only
), active AS (
    SELECT task_id,parent_task_id FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids)
    WHERE (SELECT active_only FROM mode)
      AND state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED')
), ancestors AS (
    SELECT * FROM active
    UNION
    SELECT p.task_id,p.parent_task_id FROM ancestors a
    JOIN LATERAL (
        SELECT task_id,parent_task_id FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids)
        WHERE task_id=a.parent_task_id LIMIT 1
    ) p ON TRUE WHERE a.parent_task_id IS NOT NULL
), visible AS MATERIALIZED (
    SELECT task_id,session_id,parent_task_id,namespace||'/'||agent_name AS agent,initial_prompt
    FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids) WHERE NOT (SELECT active_only FROM mode)
    UNION ALL
    SELECT p.task_id,p.session_id,p.parent_task_id,p.namespace||'/'||p.agent_name,p.initial_prompt
    FROM ancestors a JOIN LATERAL (
        SELECT task_id,session_id,parent_task_id,namespace,agent_name,initial_prompt
        FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids) WHERE task_id=a.task_id LIMIT 1
    ) p ON TRUE WHERE (SELECT active_only FROM mode)
), roots AS (
    SELECT v.task_id,v.session_id,
        CASE WHEN p.agent=ANY(boundaries) THEN p.task_id END AS initiator_id,
        CASE WHEN p.agent=ANY(boundaries) THEN p.session_id END AS initiator_session_id
    FROM visible v LEFT JOIN visible p ON p.task_id=v.parent_task_id
    WHERE NOT v.agent=ANY(boundaries) AND (p.task_id IS NULL OR p.agent=ANY(boundaries))
      AND (needle='' OR lower(COALESCE(NULLIF(CASE WHEN p.agent=ANY(boundaries) THEN p.initial_prompt END,''),v.initial_prompt)||' '||v.task_id) LIKE needle ESCAPE E'\\')
), activity AS (
    SELECT * FROM catalog_tree_activity(ns,owner_id,other_id,tree_root,allowed_ids,ARRAY(SELECT task_id FROM roots))
)
SELECT a.anchor_id,r.session_id,r.initiator_id,r.initiator_session_id,
    a.state,a.pending_tool_approval,a.updated_at,
    CASE WHEN r.initiator_id IS NULL THEN a.member_ids ELSE a.member_ids||r.initiator_id END
FROM roots r JOIN activity a ON a.anchor_id=r.task_id
WHERE (filter_agent='' OR EXISTS(SELECT 1 FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids) v WHERE v.task_id=ANY(a.member_ids) AND v.namespace||'/'||v.agent_name=filter_agent))
  AND (jsonb_array_length(filters)=0 OR EXISTS(
      SELECT 1 FROM jsonb_to_recordset(filters) AS f(state TEXT,pending_tool_approval BOOLEAN)
      WHERE (f.state IS NULL OR f.state=a.state)
        AND (f.pending_tool_approval IS NULL OR f.pending_tool_approval=a.pending_tool_approval)))
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION catalog_task_groups(TEXT,TEXT,TEXT,UUID,UUID[],TEXT[],TEXT,TEXT,JSONB);
DROP FUNCTION catalog_tree_activity(TEXT,TEXT,TEXT,UUID,UUID[],TEXT[]);
