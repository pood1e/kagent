-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION catalog_session_activity(ns TEXT, owner_id TEXT, other_id TEXT, tree_root UUID, allowed_ids UUID[], target_agent TEXT, needle TEXT)
RETURNS TABLE(session_id UUID,state TEXT,pending_tool_approval BOOLEAN,updated_at TIMESTAMPTZ)
LANGUAGE SQL STABLE AS $$
WITH RECURSIVE candidates AS (
    SELECT session_id FROM catalog_sessions(ns,owner_id,other_id,tree_root,allowed_ids) s
    WHERE s.namespace||'/'||s.agent_name=target_agent AND needle=''
    UNION
    SELECT session_id FROM catalog_sessions(ns,owner_id,other_id,tree_root,allowed_ids) s
    WHERE s.namespace||'/'||s.agent_name=target_agent AND needle<>'' AND lower(s.session_name) LIKE needle ESCAPE E'\\'
    UNION
    SELECT session_id FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids) t
    WHERE t.namespace||'/'||t.agent_name=target_agent AND needle<>'' AND lower(t.initial_prompt) LIKE needle ESCAPE E'\\'
), sessions AS MATERIALIZED (
    SELECT * FROM catalog_sessions(ns,owner_id,other_id,tree_root,allowed_ids) s
    WHERE s.session_id IN (SELECT session_id FROM candidates)
), visible AS MATERIALIZED (
    SELECT task_id,session_id,parent_task_id,state,status_timestamp,created_at,
        pending_tool_approval,background,initial_prompt
    FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids)
    WHERE root_task_id IN (
        SELECT t.root_task_id FROM catalog_tasks(ns,owner_id,other_id,tree_root,allowed_ids) t
        WHERE t.session_id IN (SELECT session_id FROM sessions))
), reach AS (
    SELECT s.session_id,t.task_id FROM sessions s JOIN visible t ON t.session_id=s.session_id
    UNION
    SELECT r.session_id,c.task_id FROM reach r JOIN visible c ON c.parent_task_id=r.task_id
), latest AS (
    SELECT DISTINCT ON(t.session_id) t.session_id,t.task_id
    FROM visible t JOIN sessions s ON s.session_id=t.session_id
    ORDER BY t.session_id,t.created_at DESC,t.task_id
), final_activity AS (
    SELECT * FROM catalog_tree_activity(ns,owner_id,other_id,tree_root,allowed_ids,ARRAY(SELECT task_id FROM latest))
), foreground AS (
    SELECT t.session_id,t.task_id FROM visible t JOIN sessions s ON s.session_id=t.session_id
    WHERE NOT COALESCE(t.background,FALSE)
    UNION
    SELECT f.session_id,c.task_id FROM foreground f JOIN visible c ON c.parent_task_id=f.task_id
    WHERE NOT COALESCE(c.background,FALSE)
), busy AS (
    SELECT DISTINCT f.session_id FROM foreground f JOIN visible t ON t.task_id=f.task_id
    WHERE t.state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED')
), facts AS (
    SELECT r.session_id,
        bool_or(t.pending_tool_approval) AS approval,
        bool_or(t.state IN ('TASK_STATE_INPUT_REQUIRED','TASK_STATE_AUTH_REQUIRED')) AS interrupted,
        bool_or(t.state='TASK_STATE_WORKING') AS working,
        bool_or(t.state='TASK_STATE_SUBMITTED') AS submitted,
        max(t.status_timestamp) AS updated_at
    FROM reach r JOIN visible t ON t.task_id=r.task_id GROUP BY r.session_id
)
SELECT s.session_id,
    CASE WHEN f.approval OR f.interrupted THEN 'TASK_STATE_INPUT_REQUIRED'
         WHEN f.working OR b.session_id IS NOT NULL AND NOT f.submitted THEN 'TASK_STATE_WORKING'
         WHEN f.submitted THEN 'TASK_STATE_SUBMITTED' ELSE a.state END,
    COALESCE(f.approval,FALSE),
    GREATEST(s.session_updated_at,f.updated_at)
FROM sessions s LEFT JOIN facts f USING(session_id) LEFT JOIN busy b USING(session_id)
LEFT JOIN latest l USING(session_id) LEFT JOIN final_activity a ON a.anchor_id=l.task_id

$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION catalog_session_activity(TEXT,TEXT,TEXT,UUID,UUID[],TEXT,TEXT);
