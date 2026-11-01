package database

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func catalogSessionUpdatedAt(session *apiv1.Session) *time.Time {
	if session.UpdatedAt == nil {
		return nil
	}
	updated := session.UpdatedAt.AsTime()
	return &updated
}

// refreshTaskCatalogMetadata runs after public state and history have committed
// their changes within the caller's transaction and existing Session lock.
// Plain narration updates reuse indexed history; new tool sources rebuild only
// this task's published history, never another task or Session.
func refreshTaskCatalogMetadata(ctx context.Context, tx dbExecutor, historyID uuid.UUID, taskID string, afterSequence int64, previous *a2a.Task) error {
	type metadataRow struct {
		Data                []byte
		InitialPrompt       string
		PendingToolApproval bool
		PendingUserInput    bool
		ToolCallCount       int64
	}
	row, err := queryOne(ctx, tx, `
		SELECT data, initial_prompt, pending_tool_approval, pending_user_input, tool_call_count
		FROM session_task WHERE history_id = $1 AND id = $2
	`, pgx.RowToStructByName[metadataRow], historyID, taskID)
	if err != nil {
		return err
	}
	task, err := unmarshalSessionTask(row.Data)
	if err != nil {
		return err
	}
	request := catalogApprovalRequest(task)
	question := catalogQuestionRequest(task)
	rebuild := previous == nil || !slices.Equal(catalogArtifactTools(previous), catalogArtifactTools(task))
	if previous != nil {
		before := catalogApprovalRequest(previous)
		rebuild = rebuild || (request != nil && (!proto.Equal(before, request) || previous.Status.Message.ID != task.Status.Message.ID))
		beforeQuestion := catalogQuestionRequest(previous)
		rebuild = rebuild || (question != nil && (!proto.Equal(beforeQuestion, question) || previous.Status.Message.ID != task.Status.Message.ID))
	}
	messages, err := queryMany(ctx, tx, `
		SELECT data FROM session_task_event
		WHERE history_id = $1 AND task_id = $2 AND sequence > $3
		  AND published AND message_id IS NOT NULL
		ORDER BY sequence
	`, pgx.RowTo[[]byte], historyID, taskID, afterSequence)
	if err != nil {
		return err
	}
	for _, data := range messages {
		event, err := unmarshalSessionTaskEvent(data)
		if err != nil {
			return err
		}
		message, ok := event.(*a2a.Message)
		if !ok {
			return fmt.Errorf("task catalog history contains %T", event)
		}
		rebuild = rebuild || len(catalogToolKeys(message.Parts, "message:"+message.ID)) > 0 ||
			(row.InitialPrompt == "" && message.Role == a2a.MessageRoleUser) ||
			catalogHITLKind(message) == apia2a.HITLTypeToolApprovalResponse ||
			catalogHITLKind(message) == apia2a.HITLTypeAskUserResponse
	}
	metadata := taskCatalogMetadata{InitialPrompt: row.InitialPrompt, PendingToolApproval: request != nil && row.PendingToolApproval, PendingUserInput: question != nil && row.PendingUserInput, ToolCallCount: row.ToolCallCount}
	if rebuild {
		if err := loadSessionTaskHistories(ctx, tx, historyID, []*a2a.Task{task}, nil, false); err != nil {
			return err
		}
		metadata = projectTaskCatalog(task)
	}
	return execSQL(ctx, tx, `
		UPDATE session_task SET initial_prompt = $3, pending_tool_approval = $4, tool_call_count = $5, pending_user_input = $6
		WHERE history_id = $1 AND id = $2
		  AND (initial_prompt, pending_tool_approval, tool_call_count, pending_user_input) IS DISTINCT FROM ($3::text, $4::boolean, $5::bigint, $6::boolean)
	`, historyID, taskID, metadata.InitialPrompt, metadata.PendingToolApproval, metadata.ToolCallCount, metadata.PendingUserInput)
}
