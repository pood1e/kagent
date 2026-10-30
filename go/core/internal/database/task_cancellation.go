package database

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TaskCancellation is a leased cleanup request. Claims survive observer loss
// and controller replacement; task ancestry is immutable before it is queued.
type TaskCancellation struct {
	TaskID     string
	SessionID  string
	ExecutorID uuid.UUID
}

// RequestTaskCancellation atomically fences further delegation, queues every
// descendant, and abandons associated resumptions, including background work
// below an already terminal parent. Callers authorize the initiating Session.
func (c *Client) RequestTaskCancellation(ctx context.Context, sessionID, taskID string) error {
	return c.withTx(ctx, func(tx pgx.Tx) error {
		root, err := queryOne(ctx, tx, `
 SELECT e.root_task_id FROM task_execution e JOIN session s ON s.history_id = e.history_id WHERE e.task_id = $1 AND s.id = $2
 `, pgx.RowTo[string], taskID, sessionID)
		if err != nil {
			return notFoundOr(err)
		}
		if err := lockExecutionRoot(ctx, tx, root); err != nil {
			return err
		}
		return queueTaskCancellations(ctx, tx, []string{taskID})
	})
}

// queueTaskCancellations fences complete subtrees and abandons their resumptions.
// Callers hold the execution-root lock and commit it with the owning transition.
func queueTaskCancellations(ctx context.Context, tx pgx.Tx, roots []string) error {
	if err := execSQL(ctx, tx, `
 WITH RECURSIVE descendants AS (
   SELECT task_id FROM task_execution WHERE task_id = ANY($1::text[])
   UNION ALL SELECT e.task_id FROM task_execution e JOIN descendants d ON e.parent_task_id = d.task_id
 ) INSERT INTO task_cancellation (task_id, completed)
 SELECT d.task_id, t.state IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_REJECTED','TASK_STATE_CANCELED')
 FROM descendants d JOIN session_task t ON t.id = d.task_id ON CONFLICT (task_id) DO NOTHING
 `, roots); err != nil {
		return err
	}
	return execSQL(ctx, tx, `
 WITH RECURSIVE descendants AS (
   SELECT task_id FROM task_execution WHERE task_id = ANY($1::text[])
   UNION ALL SELECT e.task_id FROM task_execution e JOIN descendants d ON e.parent_task_id = d.task_id
 ) UPDATE task_continuation SET abandoned = TRUE, claim_token = NULL, lease_until = NULL
 WHERE NOT delivered AND NOT abandoned AND (
   parent_task_id IN (SELECT task_id FROM descendants)
   OR child_task_id IN (SELECT task_id FROM descendants))
 `, roots)
}

// cancelUnfinishedDelegations releases foreground work when its owner settles.
// Completed synchronous MCP calls retain their registered native continuation.
// A failed owner abandons those dependencies along with the unfinished subtree.
func cancelUnfinishedDelegations(ctx context.Context, tx pgx.Tx, parentID, state string) error {
	roots, err := queryMany(ctx, tx, `
 SELECT e.task_id FROM task_execution e JOIN session_task t ON t.id = e.task_id
 WHERE e.parent_task_id = $1 AND e.relation = 'TASK_RELATION_DELEGATION' AND NOT e.background
   AND t.state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_REJECTED','TASK_STATE_CANCELED')
   AND ($2 <> 'TASK_STATE_COMPLETED' OR NOT EXISTS (
     SELECT 1 FROM task_continuation d WHERE d.parent_task_id = $1 AND d.child_task_id = e.task_id
       AND NOT d.delivered AND NOT d.abandoned))
 `, pgx.RowTo[string], parentID, state)
	if err != nil || len(roots) == 0 {
		return err
	}
	return queueTaskCancellations(ctx, tx, roots)
}

// ClaimTaskCancellations claims bounded cleanup work without retaining a
// database lock while the controller contacts an Actor.
func (c *Client) ClaimTaskCancellations(ctx context.Context, limit int) ([]TaskCancellation, error) {
	return queryMany(ctx, c.db, `
 WITH pending AS (
 SELECT task_id FROM task_cancellation WHERE NOT completed
   AND (executor_expires_at IS NULL OR executor_expires_at < clock_timestamp())
 ORDER BY task_id FOR UPDATE SKIP LOCKED LIMIT $1
 ), claimed AS (
 UPDATE task_cancellation c SET executor_id = $2, executor_expires_at = clock_timestamp() + INTERVAL '30 seconds'
 FROM pending p WHERE c.task_id = p.task_id RETURNING c.task_id, c.executor_id
 ) SELECT c.task_id, s.id::text AS session_id, c.executor_id FROM claimed c
 JOIN task_execution e ON e.task_id = c.task_id JOIN session s ON s.history_id = e.history_id
 `, pgx.RowToStructByName[TaskCancellation], limit, uuid.New())
}

// CompleteTaskCancellation settles only the current claim. Failed attempts keep
// their bounded lease and become eligible for recovery after its expiry.
func (c *Client) CompleteTaskCancellation(ctx context.Context, claim TaskCancellation) error {
	return execSQL(ctx, c.db, `
 UPDATE task_cancellation SET completed = TRUE, executor_id = NULL, executor_expires_at = NULL WHERE task_id = $1 AND executor_id = $2
 `, claim.TaskID, claim.ExecutorID)
}
