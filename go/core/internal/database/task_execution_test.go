package database

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestTaskExecutionUsesAdmittedOriginAndPreservesWaitingLineage(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	parent, child := continuationSession(t, client), continuationSession(t, client)
	root := continuationTask(t, client, parent, "root")
	callee := continuationTask(t, client, child, "child", &DispatchOrigin{SessionID: parent.Id, Background: true})
	execution, err := client.GetTaskExecution(t.Context(), string(callee.ID))
	require.NoError(t, err)
	require.Equal(t, &apiv1.TaskReference{SessionId: parent.Id, TaskId: string(root.ID)}, execution.Parent)
	require.Equal(t, execution.Parent, execution.Root)
	require.Equal(t, apiv1.TaskRelation_TASK_RELATION_DELEGATION, execution.Relation)
	require.True(t, execution.Background)
	require.ErrorIs(t, client.ReserveSessionDispatch(t.Context(), parent.Id, uuid.New(), "cycle", &DispatchOrigin{SessionID: child.Id}), ErrFailedPrecondition)

	settleContinuation(t, client, callee, a2a.TaskStateInputRequired)
	reply := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("yes"))
	reply.ContextID, reply.TaskID = child.Id, callee.ID
	resumeRuntimeTask(t, client, child.Id, reply)
	after, err := client.GetTaskExecution(t.Context(), string(callee.ID))
	require.NoError(t, err)
	require.Equal(t, execution, after)
	page, err := client.ListTaskExecutions(t.Context(), string(root.ID), "", 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	next, err := client.ListTaskExecutions(t.Context(), string(root.ID), page[0].Task.TaskId, 1)
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.NotEqual(t, page[0].Task.TaskId, next[0].Task.TaskId)
}

func TestCancellationIncludesBackgroundWorkBelowTerminalParents(t *testing.T) {
	pool := setupTestDB(t)
	client := NewClient(pool, "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	parent, child, nested, unrelated := continuationSession(t, client), continuationSession(t, client), continuationSession(t, client), continuationSession(t, client)
	root := continuationTask(t, client, parent, "root")
	callee := continuationTask(t, client, child, "background", &DispatchOrigin{SessionID: parent.Id, Background: true})
	leaf := continuationTask(t, client, nested, "nested", &DispatchOrigin{SessionID: child.Id, Background: true})
	other := continuationTask(t, client, unrelated, "unrelated")
	settleContinuation(t, client, root, a2a.TaskStateCompleted)
	settleContinuation(t, client, callee, a2a.TaskStateCompleted)
	settleContinuation(t, client, leaf, a2a.TaskStateInputRequired)
	require.NoError(t, client.RequestTaskCancellation(t.Context(), parent.Id, string(root.ID)))
	require.ErrorIs(t, client.RequestTaskCancellation(t.Context(), unrelated.Id, string(root.ID)), ErrNotFound)
	claims, err := NewClient(pool, "public").ClaimTaskCancellations(t.Context(), 4)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, string(leaf.ID), claims[0].TaskID)
	require.Equal(t, nested.Id, claims[0].SessionID)
	none, err := client.ClaimTaskCancellations(t.Context(), 4)
	require.NoError(t, err)
	require.Empty(t, none)
	_, err = pool.Exec(t.Context(), `UPDATE task_cancellation SET executor_expires_at = clock_timestamp() - INTERVAL '1 second' WHERE task_id = $1`, string(leaf.ID))
	require.NoError(t, err)
	retry, err := client.ClaimTaskCancellations(t.Context(), 4)
	require.NoError(t, err)
	require.Len(t, retry, 1)
	require.NotEqual(t, claims[0].ExecutorID, retry[0].ExecutorID)
	require.NoError(t, client.CompleteTaskCancellation(t.Context(), claims[0]))
	require.NoError(t, client.CompleteTaskCancellation(t.Context(), retry[0]))
	require.NoError(t, client.RequestTaskCancellation(t.Context(), parent.Id, string(root.ID)))
	none, err = client.ClaimTaskCancellations(t.Context(), 4)
	require.NoError(t, err)
	require.Empty(t, none)
	stored, _, err := client.GetVersionedSessionTask(t.Context(), unrelated.Id, string(other.ID))
	require.NoError(t, err)
	require.Equal(t, a2a.TaskStateWorking, stored.Status.State)
}

func TestCancellationSerializesWithFirstRuntimeSave(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	for range 10 {
		parent, child := continuationSession(t, client), continuationSession(t, client)
		root := continuationTask(t, client, parent, "root")
		dispatch := uuid.New()
		require.NoError(t, client.ReserveSessionDispatch(t.Context(), child.Id, dispatch, "child", &DispatchOrigin{SessionID: parent.Id, Background: true}))
		callee := newSessionTask(uuid.NewString(), "child")
		callee.ContextID = child.Id
		start, created, canceled := make(chan struct{}), make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := client.CreateRuntimeTask(t.Context(), child.Id, taskMutationHash("child"), callee, dispatch.String())
			created <- err
		}()
		go func() { <-start; canceled <- client.RequestTaskCancellation(t.Context(), parent.Id, string(root.ID)) }()
		close(start)
		err := <-created
		require.NoError(t, <-canceled)
		if err != nil {
			require.ErrorIs(t, err, ErrFailedPrecondition)
			_, err = client.GetTaskExecution(t.Context(), string(callee.ID))
			require.ErrorIs(t, err, ErrNotFound)
		} else {
			_, version, err := client.GetVersionedSessionTask(t.Context(), child.Id, string(callee.ID))
			require.NoError(t, err)
			_, err = client.UpdateSessionTask(t.Context(), child.Id, version, taskMutationHash("late work"), callee, callee, "")
			require.ErrorIs(t, err, ErrFailedPrecondition)
		}
		require.ErrorIs(t, client.RegisterTaskContinuation(t.Context(), parent.Id, child.Id, "late delegation"), ErrFailedPrecondition)
		require.ErrorIs(t, client.ReserveSessionDispatch(t.Context(), child.Id, uuid.New(), "late", &DispatchOrigin{SessionID: parent.Id}), ErrFailedPrecondition)
	}
}

func TestRuntimeTaskCannotForgeDispatchAncestry(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	session := continuationSession(t, client)
	task := newSessionTask(uuid.NewString(), "input")
	task.ContextID = session.Id
	task.Metadata = map[string]any{"parent_task_id": "forged", "root_task_id": "forged"}
	_, err := client.CreateRuntimeTask(t.Context(), session.Id, taskMutationHash("input"), task, "")
	require.ErrorIs(t, err, ErrFailedPrecondition)
	dispatch := uuid.New()
	require.NoError(t, client.ReserveSessionDispatch(t.Context(), session.Id, dispatch, "input", nil))
	_, err = client.CreateRuntimeTask(t.Context(), session.Id, taskMutationHash("input"), task, dispatch.String())
	require.NoError(t, err)
	result, err := client.GetTaskExecution(t.Context(), string(task.ID))
	require.NoError(t, err)
	require.Nil(t, result.Parent)
	require.Equal(t, result.Task, result.Root)
	require.Equal(t, apiv1.TaskRelation_TASK_RELATION_ROOT, result.Relation)
}
