package database

import (
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestCatalogTaskRecordsPageAndScopes(t *testing.T) {
	client := catalogQueryClient(t)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var sessions []*apiv1.Session
	for _, entry := range []struct {
		id    string
		state a2a.TaskState
	}{{"record-2", a2a.TaskStateCompleted}, {"record-1", a2a.TaskStateWorking}, {"record-3", a2a.TaskStateCompleted}} {
		session := catalogQuerySession(t, client, "team-a", "worker", "alice", entry.id, nil)
		sessions = append(sessions, session)
		catalogQueryTask(t, client, session, entry.id, "initial "+entry.id, at, entry.state, nil)
	}
	foreign := catalogQuerySession(t, client, "team-a", "worker", "bob", "foreign", nil)
	catalogQueryTask(t, client, foreign, "foreign", "bob", at.Add(time.Hour), a2a.TaskStateWorking, nil)
	otherNamespace := catalogQuerySession(t, client, "team-b", "worker", "alice", "other namespace", nil)
	catalogQueryTask(t, client, otherNamespace, "other-namespace", "other namespace", at.Add(time.Hour), a2a.TaskStateWorking, nil)
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", OwnerID: "alice"}, Limit: 1, View: apiv1.TaskCatalogView_TASK_CATALOG_VIEW_SUMMARY}
	var ids []string
	for range 3 {
		page, err := client.ListCatalogTaskRecords(t.Context(), query)
		require.NoError(t, err)
		require.Equal(t, 3, page.Total)
		require.Len(t, page.Tasks, 1)
		require.Empty(t, page.Tasks[0].Task.History)
		ids = append(ids, page.Tasks[0].Task.Id)
		query.AfterID = page.Tasks[0].Task.Id
		query.AfterTime = &at
	}
	require.Equal(t, []string{"record-1", "record-2", "record-3"}, ids)
	empty, err := client.ListCatalogTaskRecords(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 3, empty.Total)
	require.Empty(t, empty.Tasks)
	query.AfterID, query.AfterTime, query.Limit = "", nil, 100
	working := string(a2a.TaskStateWorking)
	query.Filters = []CatalogFilter{{State: &working}}
	live, err := client.ListCatalogTaskRecords(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{"record-1"}, catalogRecordIDs(live))
	activity, err := client.ListCatalogAgentActivity(t.Context(), query.Scope)
	require.NoError(t, err)
	require.Len(t, activity, 1)
	require.EqualValues(t, 1, activity[0].ActiveTasks)
	require.True(t, activity[0].Working)
	query.Filters = nil
	query.Scope = CatalogScope{Namespace: "team-a", OtherInitiatorID: "alice"}
	others, err := client.ListCatalogTaskRecords(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{"foreign"}, catalogRecordIDs(others))
	query.Scope = CatalogScope{Namespace: "team-b", OwnerID: "alice"}
	namespace, err := client.ListCatalogTaskRecords(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{"other-namespace"}, catalogRecordIDs(namespace))
	query.Scope = CatalogScope{Namespace: "team-a", SessionIDs: []string{sessions[0].Id}}
	query.View = apiv1.TaskCatalogView_TASK_CATALOG_VIEW_DETAILS
	selected, err := client.ListCatalogTaskRecords(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{"record-2"}, catalogRecordIDs(selected))
	require.Equal(t, "initial record-2", selected.Tasks[0].InitialPrompt)
	require.Len(t, selected.Tasks[0].Task.History, 1)
	query.Scope.SessionIDs = []string{}
	denied, err := client.ListCatalogTaskRecords(t.Context(), query)
	require.NoError(t, err)
	require.Zero(t, denied.Total)
	require.Empty(t, denied.Tasks)
	deniedActivity, err := client.ListCatalogAgentActivity(t.Context(), query.Scope)
	require.NoError(t, err)
	require.Empty(t, deniedActivity)
}

func TestCatalogTaskGroupsLiteralSearchAndPaging(t *testing.T) {
	client := catalogQueryClient(t)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for index, prompt := range []string{"MiXeD 100%_exact", "mixed 100xxexact", "ordinary"} {
		session := catalogQuerySession(t, client, "team-a", "worker", "alice", "", nil)
		id := []string{"group-1", "group-2", "group-3"}[index]
		catalogQueryTask(t, client, session, id, prompt, at, a2a.TaskStateCompleted, nil)
	}
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", OwnerID: "alice"}, Limit: 1, Query: "100%_"}
	literal, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 1, literal.Total)
	require.Equal(t, []string{"group-1"}, catalogGroupIDs(literal))
	query.Query = "MIXED"
	first, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 2, first.Total)
	require.Equal(t, []string{"group-1"}, catalogGroupIDs(first))
	require.Equal(t, "group-1", first.NextID)
	query.AfterID, query.AfterTime = first.NextID, first.NextTime
	second, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 2, second.Total)
	require.Equal(t, []string{"group-2"}, catalogGroupIDs(second))
	query.AfterID, query.AfterTime = "group-2", &at
	empty, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 2, empty.Total)
	require.Empty(t, empty.Groups)
	query.AfterID, query.AfterTime, query.Query = "", nil, "group-3"
	byID, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{"group-3"}, catalogGroupIDs(byID))
	query.Query = "absent"
	none, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Zero(t, none.Total)
	require.Empty(t, none.Tasks)
}
