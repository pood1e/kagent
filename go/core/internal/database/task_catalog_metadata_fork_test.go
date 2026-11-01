package database

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestTaskCatalogMetadataForkRebuildsPublishedHistory(t *testing.T) {
	client, source, task, version := catalogRuntimeFixture(t)
	task.History = append(task.History, a2a.NewMessage(a2a.MessageRoleAgent, catalogCallPart(t, "call")))
	task.Status = a2a.TaskStatus{State: a2a.TaskStateCompleted}
	version, err := client.UpdateSessionTask(t.Context(), source.Id, version, taskMutationHash("completed"), task, task, "")
	require.NoError(t, err)
	require.EqualValues(t, 0, catalogCalls(t, client, source.Id), "unpublished boundary history stays outside the index")
	require.NoError(t, client.SettleSessionTask(t.Context(), source.Id, string(task.ID), version))
	require.EqualValues(t, 1, catalogCalls(t, client, source.Id))
	work, err := client.ClaimSessionQuiescence(t.Context())
	require.NoError(t, err)
	require.NoError(t, client.FinishSessionQuiescence(t.Context(), work, &SessionTaskSnapshot{
		Atespace: "team-a", URI: "s3://snapshot/metadata", Fidelity: "SNAPSHOT_FIDELITY_VOLUMES",
	}))
	checkpoint, _, err := client.ReserveSessionCheckpoint(t.Context(), &apiv1.Checkpoint{
		Id: uuid.NewString(), SessionId: source.Id, HeadTaskId: string(task.ID),
	}, "alice", uuid.NewString())
	require.NoError(t, err)
	_, err = client.FinalizeSessionCheckpoint(t.Context(), checkpoint.Id, "tag", "s3://snapshot/retained", "")
	require.NoError(t, err)
	_, err = client.UpdateCheckpointName(t.Context(), checkpoint.Id, "alice", "fork display")
	require.NoError(t, err)
	fork, _, err := client.ForkSession(t.Context(), checkpoint.Id, "alice", "fork", uuid.NewString(), nil)
	require.NoError(t, err)
	records, err := client.ListCatalogTaskRecords(t.Context(), CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", SessionIDs: []string{fork.Id}}, Limit: 100,
		View: apiv1.TaskCatalogView_TASK_CATALOG_VIEW_DETAILS,
	})
	require.NoError(t, err)
	require.Len(t, records.Tasks, 1)
	require.NotEqual(t, string(task.ID), records.Tasks[0].Task.Id)
	require.Equal(t, "initial prompt", records.Tasks[0].InitialPrompt)
	require.False(t, records.Tasks[0].PendingToolApproval)
	require.EqualValues(t, 1, catalogCalls(t, client, fork.Id))
	page, err := client.ListCatalogSessionActivity(t.Context(), CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", SessionIDs: []string{fork.Id}}, Agent: fork.Agent,
		Query: "fork", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, 1, page.Total, "fork names are indexed in the same transaction")
	want := projectTaskCatalog(task)
	public, err := client.GetSessionTask(t.Context(), fork.Id, records.Tasks[0].Task.Id, nil)
	require.NoError(t, err)
	require.Equal(t, want, projectTaskCatalog(public))
}

func TestTaskCatalogSessionMetadataFollowsRenameAndLifecycle(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	session, _, err := client.CreateSession(t.Context(), newSessionRequest(uuid.NewString(), "assistant", "kagent", "original name"), uuid.NewString())
	require.NoError(t, err)
	query := CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", SessionIDs: []string{session.Id}}, Agent: session.Agent, Query: "original", Limit: 10,
	}
	created, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 1, created.Total)
	require.Len(t, created.Sessions, 1)
	require.Nil(t, created.Sessions[0].Activity.State, "an empty conversation has no observed A2A state")
	session, err = markSessionReady(t.Context(), client, session.Id, "agent.example")
	require.NoError(t, err)
	ready, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.GreaterOrEqual(t, ready.Sessions[0].Activity.UpdatedAt.AsTime().UnixMicro(), created.Sessions[0].Activity.UpdatedAt.AsTime().UnixMicro())
	_, err = client.UpdateSessionName(t.Context(), session.Id, "alice", "renamed conversation")
	require.NoError(t, err)
	old, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Zero(t, old.Total)
	query.Query = "renamed"
	renamed, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 1, renamed.Total)
	operation, err := client.BeginSessionOperation(t.Context(), session.Id, apiv1.RuntimeOperation_RUNTIME_OPERATION_SUSPEND)
	require.NoError(t, err)
	begin, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, operation.Instance.UpdatedAt.AsTime().UnixMicro(), begin.Sessions[0].Activity.UpdatedAt.AsTime().UnixMicro())
	executor := uuid.New()
	claimed, err := client.ClaimSessionOperation(t.Context(), session.Id, operation.ID, executor)
	require.NoError(t, err)
	require.True(t, claimed)
	suspended, err := client.FinishSessionOperation(t.Context(), session.Id, operation.ID, executor, "", "", "")
	require.NoError(t, err)
	finished, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, suspended.UpdatedAt.AsTime().UnixMicro(), finished.Sessions[0].Activity.UpdatedAt.AsTime().UnixMicro())
}
