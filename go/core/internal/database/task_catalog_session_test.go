package database

import (
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestCatalogSessionActivitySearchPagingAndObservedState(t *testing.T) {
	client := catalogQueryClient(t)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	empty := catalogQuerySession(t, client, "team-a", "worker", "alice", "Case 100%_exact", nil)
	catalogQuerySession(t, client, "team-a", "worker", "alice", "Case 100xxexact", nil)
	unknown := catalogQuerySession(t, client, "team-a", "worker", "alice", "unknown", nil)
	catalogQueryTask(t, client, unknown, "unknown", "unknown", at, a2a.TaskStateUnspecified, nil)
	searched := catalogQuerySession(t, client, "team-a", "worker", "alice", "plain name", nil)
	turn := catalogQueryTask(t, client, searched, "searched", "SeArCh %_", at.Add(time.Second), a2a.TaskStateWorking, nil)
	turn.History = append(turn.History, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("later-only text")))
	catalogQueryState(t, client, turn, a2a.TaskStateWorking, at.Add(time.Second))
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"}, Agent: empty.GetAgent().GetRef(), Limit: 100}
	all, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 4, all.Total)
	var ids []string
	for _, row := range all.Sessions {
		ids = append(ids, row.Session.Id)
		if row.Session.Id == empty.Id {
			require.Nil(t, row.Activity.State)
		}
		if row.Session.Id == unknown.Id {
			require.NotNil(t, row.Activity.State)
			require.Equal(t, a2apb.TaskState_TASK_STATE_WORKING, row.Activity.GetState(), "an observed UNSPECIFIED foreground task is still busy")
		}
	}
	query.Limit = 1
	var paged []string
	for range len(ids) {
		page, err := client.ListCatalogSessionActivity(t.Context(), query)
		require.NoError(t, err)
		require.Equal(t, 4, page.Total)
		require.Len(t, page.Sessions, 1)
		paged = append(paged, page.Sessions[0].Session.Id)
		query.AfterID = page.Sessions[0].Session.Id
		at := page.Sessions[0].Activity.UpdatedAt.AsTime()
		query.AfterTime = &at
	}
	require.Equal(t, ids, paged)
	page, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 4, page.Total)
	require.Empty(t, page.Sessions)
	query.AfterID, query.AfterTime, query.Limit = "", nil, 100
	for _, test := range []struct {
		query string
		ids   []string
	}{{"100%_", []string{empty.Id}}, {"SEARCH", []string{searched.Id}}, {"later-only", nil}} {
		query.Query = test.query
		page, err := client.ListCatalogSessionActivity(t.Context(), query)
		require.NoError(t, err)
		require.Equal(t, len(test.ids), page.Total)
		var got []string
		for _, row := range page.Sessions {
			got = append(got, row.Session.Id)
		}
		require.ElementsMatch(t, test.ids, got)
	}
	records, err := client.ListCatalogTaskRecords(t.Context(), CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", SessionIDs: []string{unknown.Id}}, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, a2apb.TaskState_TASK_STATE_UNSPECIFIED, records.Tasks[0].Task.Status.State)
}

func TestCatalogSessionActivityPreservesMissingTimestamp(t *testing.T) {
	client := catalogQueryClient(t)
	for range 2 {
		id := uuid.NewString()
		require.NoError(t, client.withTx(t.Context(), func(tx pgx.Tx) error {
			_, err := insertSessionRecords(t.Context(), tx, &apiv1.Session{
				Id: id, ContextId: id, Creator: "alice", Name: "retained session",
				Agent: &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: "worker"}}, PreparedRevision: "team-a-worker",
				State: apiv1.RuntimeState_RUNTIME_STATE_CREATING, Operation: apiv1.RuntimeOperation_RUNTIME_OPERATION_CREATE,
				Authority: catalogAuthority(id, "alice", "team-a"),
			}, uuid.NewString(), uuid.New(), nil)
			return err
		}))
	}
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"}, Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "worker"}, Limit: 1}
	first, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 2, first.Total)
	require.Len(t, first.Sessions, 1)
	require.Nil(t, first.Sessions[0].Activity.State)
	require.Nil(t, first.Sessions[0].Activity.UpdatedAt)
	epoch := time.Unix(0, 0).UTC()
	query.AfterID, query.AfterTime = first.NextID, &epoch
	second, err := client.ListCatalogSessionActivity(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, second.Sessions, 1)
	require.NotEqual(t, first.Sessions[0].Session.Id, second.Sessions[0].Session.Id)
	require.Nil(t, second.Sessions[0].Activity.UpdatedAt)
}

func TestCatalogSessionActivityLatestTurnUsesCreationTime(t *testing.T) {
	client := catalogQueryClient(t)
	session := catalogQuerySession(t, client, "team-a", "worker", "alice", "turns", nil)
	latestStatus := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	catalogQueryTask(t, client, session, "older-turn", "older", latestStatus, a2a.TaskStateCompleted, nil)
	catalogQueryTask(t, client, session, "newer-turn", "newer", latestStatus.Add(-time.Hour), a2a.TaskStateFailed, nil)
	page, err := client.ListCatalogSessionActivity(t.Context(), CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"}, Agent: session.GetAgent().GetRef(), Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Sessions, 1)
	require.Equal(t, a2apb.TaskState_TASK_STATE_FAILED, page.Sessions[0].Activity.GetState())
	require.Equal(t, latestStatus, page.Sessions[0].Activity.UpdatedAt.AsTime())
	activity, err := client.ListCatalogAgentActivity(t.Context(), CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"})
	require.NoError(t, err)
	require.Len(t, activity, 1)
	require.Zero(t, activity[0].ActiveTasks)
	require.Zero(t, activity[0].ToolCalls)
	require.False(t, activity[0].Working)
	require.False(t, activity[0].PendingToolApproval)
	require.False(t, activity[0].Interrupted)
}
