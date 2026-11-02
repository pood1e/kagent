package database

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestTaskCatalogMetadataPublishedBoundary(t *testing.T) {
	client, session, task, version := catalogRuntimeFixture(t)
	require.Equal(t, "initial prompt", catalogTask(t, client, session.Id, string(task.ID)).InitialPrompt)
	call := a2a.NewMessage(a2a.MessageRoleAgent, catalogCallPart(t, "call"))
	task.History = append(task.History, call, call)
	version, err := client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("call"), task, task, "")
	require.NoError(t, err)
	require.EqualValues(t, 1, catalogCalls(t, client, session.Id))

	request := a2a.NewMessage(a2a.MessageRoleAgent)
	require.NoError(t, apia2a.AttachHITL(request, &apiv1.ToolApprovalRequest{
		Type: apia2a.HITLTypeToolApprovalRequest, Tools: []*apiv1.HitlTool{{Id: "tool", Name: "inspect"}},
	}))
	task.Status = a2a.TaskStatus{State: a2a.TaskStateInputRequired, Message: request}
	version, err = client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("waiting"), task, task, "")
	require.NoError(t, err)
	before := catalogTask(t, client, session.Id, string(task.ID))
	require.Equal(t, a2a.TaskStateWorking, a2a.TaskState(before.Task.Status.State.String()))
	require.False(t, before.PendingToolApproval)
	require.NoError(t, client.SettleSessionTask(t.Context(), session.Id, string(task.ID), version))
	require.True(t, catalogTask(t, client, session.Id, string(task.ID)).PendingToolApproval)

	current, version, err := client.GetVersionedSessionTask(t.Context(), session.Id, string(task.ID))
	require.NoError(t, err)
	reply := a2a.NewMessage(a2a.MessageRoleUser, catalogCallPart(t, "call"))
	reply.TaskID, reply.ContextID = task.ID, task.ContextID
	require.NoError(t, apia2a.AttachHITL(reply, &apiv1.ToolApprovalResponse{
		Type: apia2a.HITLTypeToolApprovalResponse, Approvals: []*apiv1.ToolApproval{{Id: "tool", Approved: true}},
	}))
	current.History = append(current.History, reply)
	dispatch := uuid.New()
	require.NoError(t, client.ReserveSessionDispatch(t.Context(), session.Id, dispatch, reply.ID, nil))
	version, err = client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("answered"), current, current, dispatch.String())
	require.NoError(t, err)
	require.False(t, catalogTask(t, client, session.Id, string(task.ID)).PendingToolApproval)
	require.EqualValues(t, 0, catalogCalls(t, client, session.Id))
	current.Status = a2a.TaskStatus{State: a2a.TaskStateWorking}
	_, err = client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("resumed"), current, current, dispatch.String())
	require.NoError(t, err)
	require.False(t, catalogTask(t, client, session.Id, string(task.ID)).PendingToolApproval)
}

func TestTaskCatalogMetadataSkipsUnchangedHistoryAndRollsBack(t *testing.T) {
	client, session, task, version := catalogRuntimeFixture(t)
	// A deliberately malformed retained message proves ordinary narration never
	// replays old history. A new tool source must validate it and roll back.
	_, err := client.db.Exec(t.Context(), `UPDATE session_task_event SET data = $2 WHERE message_id = $1`, task.History[0].ID, []byte{0xff})
	require.NoError(t, err)
	task.Status.Message = a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("token"))
	version, err = client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("text"), task, task, "")
	require.NoError(t, err)
	before := catalogTask(t, client, session.Id, string(task.ID))
	require.Equal(t, "initial prompt", before.InitialPrompt)
	task.History = append(task.History, a2a.NewMessage(a2a.MessageRoleAgent, catalogCallPart(t, "new")))
	_, err = client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("broken tool history"), task, task, "")
	require.Error(t, err)
	row, err := readSession(t.Context(), client.db, session.Id)
	require.NoError(t, err)
	storedVersion, err := taskVersion(t.Context(), client.db, row.HistoryID, string(task.ID))
	require.NoError(t, err)
	require.Equal(t, version, storedVersion)
	require.EqualValues(t, 0, catalogCalls(t, client, session.Id))
	require.Equal(t, before.InitialPrompt, catalogTask(t, client, session.Id, string(task.ID)).InitialPrompt)
}

func TestTaskCatalogMetadataConcurrentCancellation(t *testing.T) {
	client, session, task, version := catalogRuntimeFixture(t)
	task.History = append(task.History, a2a.NewMessage(a2a.MessageRoleAgent, catalogCallPart(t, "call")))
	start, saved, canceled := make(chan struct{}), make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, err := client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("racing call"), task, task, "")
		saved <- err
	}()
	go func() {
		<-start
		canceled <- client.RequestTaskCancellation(t.Context(), session.Id, string(task.ID))
	}()
	close(start)
	saveErr := <-saved
	require.NoError(t, <-canceled)
	if saveErr != nil {
		require.ErrorIs(t, saveErr, ErrFailedPrecondition)
	}
	current, version, err := client.GetVersionedSessionTask(t.Context(), session.Id, string(task.ID))
	require.NoError(t, err)
	want := projectTaskCatalog(current)
	require.EqualValues(t, want.ToolCallCount, catalogCalls(t, client, session.Id))
	current.Status = a2a.TaskStatus{State: a2a.TaskStateCanceled}
	version, err = client.UpdateSessionTask(t.Context(), session.Id, version, taskMutationHash("canceled"), current, current, "")
	require.NoError(t, err)
	require.NoError(t, client.SettleSessionTask(t.Context(), session.Id, string(task.ID), version))
	require.False(t, catalogTask(t, client, session.Id, string(task.ID)).PendingToolApproval)
	require.EqualValues(t, want.ToolCallCount, catalogCalls(t, client, session.Id))
}

func catalogRuntimeFixture(t *testing.T) (*Client, *apiv1.Session, *a2a.Task, int64) {
	t.Helper()
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	request := newSessionRequest(uuid.NewString(), "assistant", "kagent", "conversation")
	request.Authority = catalogAuthority(request.Id, request.Creator, request.GetAgent().GetRef().GetNamespace())
	session, _, err := client.CreateSession(t.Context(), request, uuid.NewString())
	require.NoError(t, err)
	session, err = markSessionReady(t.Context(), client, session.Id, "agent.example")
	require.NoError(t, err)
	task := newSessionTask(uuid.NewString(), uuid.NewString())
	task.ContextID = session.Id
	task.History[0].Parts = a2a.ContentParts{a2a.NewTextPart("initial prompt")}
	version, err := createRuntimeTask(client, t.Context(), session.Id, taskMutationHash("create"), task, "")
	require.NoError(t, err)
	return client, session, task, version
}

func catalogTask(t *testing.T, client *Client, sessionID, taskID string) *apiv1.CatalogTask {
	t.Helper()
	result, err := client.ListCatalogTaskRecords(t.Context(), CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", SessionIDs: []string{sessionID}}, Limit: 100,
		View: apiv1.TaskCatalogView_TASK_CATALOG_VIEW_SUMMARY,
	})
	require.NoError(t, err)
	for _, task := range result.Tasks {
		if task.Task.Id == taskID {
			return task
		}
	}
	t.Fatalf("catalog omitted task %s", taskID)
	return nil
}

func catalogCalls(t *testing.T, client *Client, sessionID string) uint64 {
	t.Helper()
	rows, err := client.ListCatalogAgentActivity(t.Context(), CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", SessionIDs: []string{sessionID}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0].ToolCalls
}
