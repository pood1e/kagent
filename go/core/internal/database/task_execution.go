package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// DispatchOrigin is derived by native services from a verified runtime or a
// durable continuation. Public callers cannot select parent or root task IDs.
type DispatchOrigin struct {
	SessionID      string
	ContinuationID uuid.UUID
	Background     bool
}

type executionRow struct {
	TaskID           string
	SessionID        string
	ParentTaskID     *string
	ParentSessionID  *string
	RootTaskID       string
	RootSessionID    string
	Relation         string
	Background       bool
	ResumedTaskID    *string
	ResumedSessionID *string
}

func (r executionRow) proto() *apiv1.TaskExecution {
	result := &apiv1.TaskExecution{Task: &apiv1.TaskReference{SessionId: r.SessionID, TaskId: r.TaskID}, Root: &apiv1.TaskReference{SessionId: r.RootSessionID, TaskId: r.RootTaskID}, Background: r.Background, Relation: apiv1.TaskRelation(apiv1.TaskRelation_value[r.Relation])}
	if r.ParentTaskID != nil {
		result.Parent = &apiv1.TaskReference{SessionId: *r.ParentSessionID, TaskId: *r.ParentTaskID}
	}
	if r.ResumedTaskID != nil {
		result.ResumedTask = &apiv1.TaskReference{SessionId: *r.ResumedSessionID, TaskId: *r.ResumedTaskID}
	}
	return result
}

// GetTaskExecution returns the canonical relationship, independently of caller
// metadata. Callers authorize the owning Session before publishing it.
func (c *Client) GetTaskExecution(ctx context.Context, taskID string) (*apiv1.TaskExecution, error) {
	row, err := queryOne(ctx, c.db, `
 SELECT e.task_id, s.id::text AS session_id, e.parent_task_id, p.id::text AS parent_session_id,
   e.root_task_id, r.id::text AS root_session_id, e.relation, e.background,
 e.resumed_task_id, resumed_session.id::text AS resumed_session_id
 FROM task_execution e JOIN session s ON s.history_id = e.history_id
 JOIN task_execution root ON root.task_id = e.root_task_id JOIN session r ON r.history_id = root.history_id
 LEFT JOIN task_execution parent ON parent.task_id = e.parent_task_id LEFT JOIN session p ON p.history_id = parent.history_id
 LEFT JOIN task_execution resumed ON resumed.task_id=e.resumed_task_id LEFT JOIN session resumed_session ON resumed_session.history_id=resumed.history_id
 WHERE e.task_id = $1
 `, pgx.RowToStructByName[executionRow], taskID)
	if err != nil {
		return nil, notFoundOr(err)
	}
	return row.proto(), nil
}

// ListTaskExecutions returns one deterministic page in an execution tree.
// Authorization and public cursor handling belong to the execution service.
func (c *Client) ListTaskExecutions(ctx context.Context, rootTaskID, after string, limit int) ([]*apiv1.TaskExecution, error) {
	rows, err := queryMany(ctx, c.db, `
 SELECT e.task_id, s.id::text AS session_id, e.parent_task_id, p.id::text AS parent_session_id,
   e.root_task_id, r.id::text AS root_session_id, e.relation, e.background,
 e.resumed_task_id, resumed_session.id::text AS resumed_session_id
 FROM task_execution e JOIN session s ON s.history_id = e.history_id
 JOIN task_execution root ON root.task_id = e.root_task_id JOIN session r ON r.history_id = root.history_id
 LEFT JOIN task_execution parent ON parent.task_id = e.parent_task_id LEFT JOIN session p ON p.history_id = parent.history_id
 LEFT JOIN task_execution resumed ON resumed.task_id=e.resumed_task_id LEFT JOIN session resumed_session ON resumed_session.history_id=resumed.history_id
 WHERE e.root_task_id = $1 AND e.task_id > $2 ORDER BY e.task_id LIMIT $3
 `, pgx.RowToStructByName[executionRow], rootTaskID, after, limit)
	if err != nil {
		return nil, err
	}
	result := make([]*apiv1.TaskExecution, len(rows))
	for i, row := range rows {
		result[i] = row.proto()
	}
	return result, nil
}

func lockExecutionRoot(ctx context.Context, tx pgx.Tx, root string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 191003))`, root)
	return err
}

func runtimeExecutionLock(ctx context.Context, tx pgx.Tx, taskID, dispatchID string) error {
	root, err := queryOne(ctx, tx, `
 SELECT COALESCE((SELECT root_task_id FROM task_execution WHERE task_id = $1),
   (SELECT root_task_id FROM task_dispatch WHERE dispatch_id::text = $2), $1)
 `, pgx.RowTo[string], taskID, dispatchID)
	if err != nil {
		return err
	}
	return lockExecutionRoot(ctx, tx, root)
}

func bindTaskExecution(ctx context.Context, tx pgx.Tx, session sessionRow, taskID, dispatchID string) error {
	type intent struct {
		ParentTaskID  *string
		RootTaskID    *string
		Relation      string
		Background    bool
		ResumedTaskID *string
	}
	value, err := queryOne(ctx, tx, `
 SELECT parent_task_id, root_task_id, relation, background,resumed_task_id FROM task_dispatch WHERE session_id = $1 AND dispatch_id::text = $2
 `, pgx.RowToStructByName[intent], session.ID, dispatchID)
	if err != nil {
		return fmt.Errorf("task creation requires admitted dispatch lineage: %w", notFoundOr(err))
	}
	if value.ParentTaskID != nil {
		canceled, err := queryOne(ctx, tx, `SELECT EXISTS (SELECT 1 FROM task_cancellation WHERE task_id = $1)`, pgx.RowTo[bool], *value.ParentTaskID)
		if err != nil {
			return err
		}
		if canceled {
			return ErrFailedPrecondition
		}
		if !value.Background && value.Relation == apiv1.TaskRelation_TASK_RELATION_DELEGATION.String() {
			admitted, err := queryOne(ctx, tx, `
 SELECT EXISTS (SELECT 1 FROM session_task p WHERE p.id = $1 AND (
   p.state = 'TASK_STATE_WORKING' OR (p.state = 'TASK_STATE_COMPLETED' AND EXISTS (
     SELECT 1 FROM task_continuation d JOIN session_task_event e
       ON e.history_id = $2 AND e.task_id = $3 AND e.message_id = d.child_message_id
     WHERE d.parent_task_id = p.id AND d.child_session_id = $4 AND NOT d.delivered AND NOT d.abandoned))))
 `, pgx.RowTo[bool], *value.ParentTaskID, session.HistoryID, taskID, session.ID)
			if err != nil {
				return err
			}
			if !admitted {
				return ErrFailedPrecondition
			}
		}
	}
	root := taskID
	if value.RootTaskID != nil {
		root = *value.RootTaskID
	}
	if err := execSQL(ctx, tx, `
 INSERT INTO task_execution (task_id, history_id, parent_task_id, root_task_id, relation, background,resumed_task_id) VALUES ($1, $2, $3, $4, $5, $6,$7)
 `, taskID, session.HistoryID, value.ParentTaskID, root, value.Relation, value.Background, value.ResumedTaskID); err != nil {
		return err
	}
	return execSQL(ctx, tx, `DELETE FROM task_dispatch WHERE session_id = $1 AND dispatch_id::text = $2`, session.ID, dispatchID)
}

func dispatchParent(ctx context.Context, tx pgx.Tx, origin *DispatchOrigin, targetID, messageID string) (*apiv1.TaskExecution, error) {
	if origin == nil {
		return nil, nil
	}
	var parentID string
	var err error
	if origin.ContinuationID != uuid.Nil {
		parentID, err = queryOne(ctx, tx, `
 SELECT parent_task_id FROM task_continuation WHERE id = $1 AND parent_session_id = $2 AND NOT abandoned
 `, pgx.RowTo[string], origin.ContinuationID, targetID)
		if messageID != "kagent-continuation-"+origin.ContinuationID.String() {
			return nil, ErrFailedPrecondition
		}
	} else {
		parentID, err = queryOne(ctx, tx, `
 SELECT t.id FROM session_task t JOIN session s ON s.history_id = t.history_id WHERE s.id = $1 AND t.state = 'TASK_STATE_WORKING'
 `, pgx.RowTo[string], origin.SessionID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("delegation requires an admitted parent task: %w", ErrFailedPrecondition)
		}
		return nil, err
	}
	root, err := queryOne(ctx, tx, `SELECT root_task_id FROM task_execution WHERE task_id = $1`, pgx.RowTo[string], parentID)
	if err != nil {
		return nil, notFoundOr(err)
	}
	if err := lockExecutionRoot(ctx, tx, root); err != nil {
		return nil, err
	}
	blocked, err := queryOne(ctx, tx, `
 WITH RECURSIVE ancestors AS (
   SELECT task_id, history_id, parent_task_id, 1 AS depth FROM task_execution WHERE task_id = $1
   UNION ALL SELECT e.task_id, e.history_id, e.parent_task_id, a.depth + 1 FROM task_execution e JOIN ancestors a ON a.parent_task_id = e.task_id
 ) SELECT EXISTS (SELECT 1 FROM ancestors a JOIN session s ON s.history_id = a.history_id
    WHERE a.depth >= 32 OR ($3 AND s.id = $2) OR EXISTS (SELECT 1 FROM task_cancellation c WHERE c.task_id = a.task_id))
 `, pgx.RowTo[bool], parentID, targetID, origin.ContinuationID == uuid.Nil)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, ErrFailedPrecondition
	}
	if origin.ContinuationID == uuid.Nil {
		state, err := queryOne(ctx, tx, `SELECT state FROM session_task WHERE id = $1`, pgx.RowTo[string], parentID)
		if err != nil {
			return nil, err
		}
		if state != "TASK_STATE_WORKING" {
			return nil, ErrFailedPrecondition
		}
	}
	return &apiv1.TaskExecution{Parent: &apiv1.TaskReference{TaskId: parentID}, Root: &apiv1.TaskReference{TaskId: root}}, nil
}

func recordTaskDispatch(ctx context.Context, tx pgx.Tx, sessionID, dispatchID uuid.UUID, parent *apiv1.TaskExecution, origin *DispatchOrigin) error {
	var parentID, rootID *string
	relation, background := apiv1.TaskRelation_TASK_RELATION_ROOT.String(), false
	if parent != nil {
		parentID, rootID = &parent.Parent.TaskId, &parent.Root.TaskId
		relation, background = apiv1.TaskRelation_TASK_RELATION_DELEGATION.String(), origin.Background
		if origin.ContinuationID != uuid.Nil {
			relation, background = apiv1.TaskRelation_TASK_RELATION_CONTINUATION.String(), false
		}
	}
	continuationID := uuid.Nil
	if origin != nil {
		continuationID = origin.ContinuationID
	}
	return execSQL(ctx, tx, `
 INSERT INTO task_dispatch (session_id, dispatch_id, parent_task_id, root_task_id, relation, background,resumed_task_id) VALUES ($1,$2,$3,$4,$5,$6, (SELECT child_task_id FROM task_continuation WHERE id=$7))
 ON CONFLICT (session_id) DO UPDATE SET dispatch_id = EXCLUDED.dispatch_id, parent_task_id = EXCLUDED.parent_task_id,
 root_task_id = EXCLUDED.root_task_id, relation = EXCLUDED.relation, background = EXCLUDED.background,resumed_task_id=EXCLUDED.resumed_task_id
 `, sessionID, dispatchID, parentID, rootID, relation, background, continuationID)
}
