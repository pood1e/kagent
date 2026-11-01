package database

import (
	"context"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2apb/v1/pbconv"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type catalogTaskRow struct {
	SessionID           string
	Creator             string
	RuntimeState        string
	Operation           string
	PreparedRevision    string
	SessionData         []byte
	TaskID              string
	State               string
	StatusTimestamp     *time.Time
	CreatedAt           time.Time
	InitialPrompt       string
	PendingToolApproval bool
	Data                []byte
	ParentTaskID        *string
	ParentSessionID     *string
	RootTaskID          *string
	RootSessionID       *string
	Relation            *string
	Background          *bool
	ResumedTaskID       *string
	ResumedSessionID    *string
}

func catalogSession(row catalogTaskRow) (*apiv1.Session, error) {
	value := &apiv1.Session{}
	if err := proto.Unmarshal(row.SessionData, value); err != nil {
		return nil, fmt.Errorf("decode catalog Session: %w", err)
	}
	state, ok := apiv1.RuntimeState_value[row.RuntimeState]
	if !ok {
		return nil, fmt.Errorf("unknown catalog runtime state %q", row.RuntimeState)
	}
	operation, ok := apiv1.RuntimeOperation_value[row.Operation]
	if !ok {
		return nil, fmt.Errorf("unknown catalog runtime operation %q", row.Operation)
	}
	value.Id = row.SessionID
	value.ContextId = row.SessionID
	value.Creator = row.Creator
	value.State = apiv1.RuntimeState(state)
	value.Operation = apiv1.RuntimeOperation(operation)
	value.PreparedRevision = row.PreparedRevision
	return value, nil
}

// readCatalogPayloads hydrates a bounded selection in two queries, including only
// published message history. The caller owns a consistent read transaction.
func readCatalogPayloads(ctx context.Context, db dbExecutor, scope CatalogScope, ids []string, view apiv1.TaskCatalogView) (CatalogRecords, error) {
	result := CatalogRecords{Tasks: []*apiv1.CatalogTask{}, Sessions: []*apiv1.Session{}}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := queryMany(ctx, db, `
 SELECT session_id::text,creator,runtime_state,operation,prepared_revision,session_data,
 task_id,state,status_timestamp,created_at,initial_prompt,pending_tool_approval,
 CASE WHEN $7::boolean OR ($8::boolean AND state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED')) THEN task_data ELSE NULL END AS data,
 parent_task_id,parent_session_id::text,root_task_id,execution_root_session_id::text AS root_session_id,
 relation,background,resumed_task_id,resumed_session_id::text
 FROM catalog_tasks($1,$2,$3,$4,$5) WHERE task_id=ANY($6::text[])
 ORDER BY array_position($6::text[],task_id)
 `, pgx.RowToStructByName[catalogTaskRow], append(scope.args(), ids, view == apiv1.TaskCatalogView_TASK_CATALOG_VIEW_DETAILS, view == apiv1.TaskCatalogView_TASK_CATALOG_VIEW_ACTIVE_DETAILS)...)
	if err != nil {
		return result, err
	}
	fullIDs := []string{}
	for _, row := range rows {
		if row.Data != nil {
			fullIDs = append(fullIDs, row.TaskID)
		}
	}
	type historyRow struct {
		TaskID string
		Data   []byte
	}
	histories := map[string][]*a2a.Message{}
	if len(fullIDs) > 0 {
		messages, err := queryMany(ctx, db, `
 SELECT task_id,data FROM session_task_event
 WHERE task_id=ANY($1::text[]) AND published AND message_id IS NOT NULL ORDER BY sequence
 `, pgx.RowToStructByName[historyRow], fullIDs)
		if err != nil {
			return result, err
		}
		for _, row := range messages {
			event, err := unmarshalSessionTaskEvent(row.Data)
			if err != nil {
				return result, err
			}
			message, ok := event.(*a2a.Message)
			if !ok {
				return result, fmt.Errorf("catalog history contains non-message event")
			}
			histories[row.TaskID] = append(histories[row.TaskID], message)
		}
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.RootTaskID == nil || row.RootSessionID == nil || row.Relation == nil || row.Background == nil {
			return result, fmt.Errorf("catalog task %s has no native execution", row.TaskID)
		}
		task := &a2a.Task{ID: a2a.TaskID(row.TaskID), ContextID: row.SessionID, Status: a2a.TaskStatus{State: a2a.TaskState(row.State), Timestamp: row.StatusTimestamp}}
		if row.Data != nil {
			task, err = unmarshalSessionTask(row.Data)
			if err != nil {
				return result, err
			}
			if history, ok := histories[row.TaskID]; ok {
				task.History = history
			}
		}
		apia2a.SetTaskCreatedAt(task, row.CreatedAt)
		wire, err := pbconv.ToProtoTask(task)
		if err != nil {
			return result, err
		}
		execution := executionRow{TaskID: row.TaskID, SessionID: row.SessionID, ParentTaskID: row.ParentTaskID, ParentSessionID: row.ParentSessionID, RootTaskID: *row.RootTaskID, RootSessionID: *row.RootSessionID, Relation: *row.Relation, Background: *row.Background, ResumedTaskID: row.ResumedTaskID, ResumedSessionID: row.ResumedSessionID}.proto()
		result.Tasks = append(result.Tasks, &apiv1.CatalogTask{Task: wire, Execution: execution, InitialPrompt: row.InitialPrompt, PendingToolApproval: row.PendingToolApproval})
		if !seen[row.SessionID] {
			session, err := catalogSession(row)
			if err != nil {
				return result, err
			}
			result.Sessions = append(result.Sessions, session)
			seen[row.SessionID] = true
		}
	}
	return result, nil
}
