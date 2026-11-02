package database

import (
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCatalogGroupsKeepOverlappingCompleteTrees(t *testing.T) {
	client := catalogQueryClient(t)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	parent := catalogQuerySession(t, client, "team-a", "worker", "alice", "", nil)
	middle := catalogQuerySession(t, client, "team-a", "boundary", "alice", "", parent)
	child := catalogQuerySession(t, client, "team-a", "worker", "alice", "", middle)
	root := catalogQueryTask(t, client, parent, "a-root", "root prompt", at, a2a.TaskStateWorking, nil)
	bridge := catalogQueryTask(t, client, middle, "b-middle", "delegated prompt", at.Add(time.Second), a2a.TaskStateWorking, &DispatchOrigin{SessionID: parent.Id})
	leaf := catalogQueryTask(t, client, child, "c-leaf", "child prompt", at.Add(2*time.Second), a2a.TaskStateWorking, &DispatchOrigin{SessionID: middle.Id})
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"}, BoundaryAgents: []string{"team-a/boundary"}, Limit: 100}
	all, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{string(root.ID), string(leaf.ID)}, catalogGroupIDs(all))
	require.Len(t, all.Tasks, 3, "overlapping groups share normalized task rows")
	require.Len(t, all.Sessions, 3)
	require.Equal(t, string(bridge.ID), all.Groups[1].Initiator.TaskId)
	for _, entry := range all.Tasks {
		if entry.Task.Id == string(leaf.ID) {
			require.Equal(t, string(root.ID), entry.Execution.Root.TaskId)
			require.Equal(t, string(bridge.ID), entry.Execution.Parent.TaskId)
		}
	}
	query.Limit = 1
	first, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 2, first.Total)
	require.Equal(t, []string{string(root.ID)}, catalogGroupIDs(first))
	require.ElementsMatch(t, []string{string(root.ID), string(bridge.ID), string(leaf.ID)}, catalogRecordIDs(first.CatalogRecords))
	query.AfterID, query.AfterTime = first.NextID, first.NextTime
	second, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{string(leaf.ID)}, catalogGroupIDs(second))
	require.ElementsMatch(t, []string{string(bridge.ID), string(leaf.ID)}, catalogRecordIDs(second.CatalogRecords))
	query.AfterID, query.AfterTime, query.Limit = "", nil, 100
	query.Agent = middle.GetAgent().GetRef()
	byAgent, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{string(root.ID)}, catalogGroupIDs(byAgent), "an initiator is not a descendant of its delegated group")
	query.Agent = nil
	query.Query = "delegated prompt"
	byInitiator, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{string(leaf.ID)}, catalogGroupIDs(byInitiator))
}

func TestCatalogParentStateUsesForegroundAndRootFirstOrder(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground", true: "background"}[background], func(t *testing.T) {
			client := catalogQueryClient(t)
			at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			parent := catalogQuerySession(t, client, "team-a", "worker", "alice", "", nil)
			child := catalogQuerySession(t, client, "team-a", "worker", "alice", "", parent)
			root := catalogQueryTask(t, client, parent, "root", "root", at, a2a.TaskStateWorking, nil)
			leaf := catalogQueryTask(t, client, child, "leaf", "leaf", at.Add(time.Second), a2a.TaskStateWorking, &DispatchOrigin{SessionID: parent.Id, Background: background})
			if background {
				nested := catalogQuerySession(t, client, "team-a", "worker", "alice", "", child)
				catalogQueryTask(t, client, nested, "nested", "nested", at.Add(time.Second), a2a.TaskStateWorking, &DispatchOrigin{SessionID: child.Id})
			}
			query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"}, Limit: 100}
			catalogQueryState(t, client, root, a2a.TaskStateSubmitted, at.Add(2*time.Second))
			submitted, err := client.ListCatalogTaskGroups(t.Context(), query)
			require.NoError(t, err)
			require.Equal(t, a2apb.TaskState_TASK_STATE_SUBMITTED, submitted.Groups[0].Activity.GetState())
			catalogQueryState(t, client, root, a2a.TaskStateCompleted, at.Add(3*time.Second))
			completed, err := client.ListCatalogTaskGroups(t.Context(), query)
			require.NoError(t, err)
			want := a2apb.TaskState_TASK_STATE_WORKING
			if background {
				want = a2apb.TaskState_TASK_STATE_COMPLETED
			}
			require.Equal(t, want, completed.Groups[0].Activity.GetState())
			records, err := client.ListCatalogTaskRecords(t.Context(), query)
			require.NoError(t, err)
			for _, entry := range records.Tasks {
				if entry.Task.Id == string(leaf.ID) {
					require.Equal(t, background, entry.Background)
				}
				if entry.Task.Id == "nested" {
					require.True(t, entry.Background)
					require.False(t, entry.Execution.Background, "inherited display activity keeps immutable edge facts")
				}
			}
			query.Agent = parent.GetAgent().GetRef()
			conversations, err := client.ListCatalogSessionActivity(t.Context(), query)
			require.NoError(t, err)
			for _, session := range conversations.Sessions {
				if session.Session.Id == parent.Id {
					require.Equal(t, a2apb.TaskState_TASK_STATE_WORKING, session.Activity.GetState(), "conversation activity includes background work")
				}
			}
		})
	}
}

func TestCatalogGroupsRetainNativeContinuations(t *testing.T) {
	client := catalogQueryClient(t)
	catalogQueryAgent(t, client, "team-a", "assistant")
	parent := continuationSession(t, client)
	child := continuationSession(t, client, parent)
	root := continuationTask(t, client, parent, "root")
	require.NoError(t, client.RegisterTaskContinuation(t.Context(), parent.Id, child.Id, "child"))
	callee := continuationTask(t, client, child, "child", &DispatchOrigin{SessionID: parent.Id})
	settleContinuation(t, client, callee, a2a.TaskStateInputRequired)
	settleContinuation(t, client, root, a2a.TaskStateCompleted)
	reply := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("yes"))
	reply.TaskID, reply.ContextID = callee.ID, child.Id
	callee, _ = resumeRuntimeTask(t, client, child.Id, reply)
	settleContinuation(t, client, callee, a2a.TaskStateCompleted)
	claims, err := client.ClaimTaskContinuations(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	resumed := continuationTask(t, client, parent, "kagent-continuation-"+claims[0].ID.String(), &DispatchOrigin{ContinuationID: claims[0].ID})
	groups, err := client.ListCatalogTaskGroups(t.Context(), CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, []string{string(root.ID)}, catalogGroupIDs(groups))
	require.Len(t, groups.Tasks, 3)
	require.Equal(t, a2apb.TaskState_TASK_STATE_WORKING, groups.Groups[0].Activity.GetState())
	for _, task := range groups.Tasks {
		if task.Task.Id == string(resumed.ID) {
			require.Equal(t, string(callee.ID), task.Execution.ResumedTask.TaskId)
			require.Equal(t, string(root.ID), task.Execution.Root.TaskId)
		}
	}
}

func TestCatalogLiveChildSurvivesDeletedNativeRoot(t *testing.T) {
	client := catalogQueryClient(t)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	parent := catalogQuerySession(t, client, "team-a", "worker", "alice", "", nil)
	child := catalogQuerySession(t, client, "team-a", "worker", "alice", "child", parent)
	root := catalogQueryTask(t, client, parent, "deleted-root", "root", at, a2a.TaskStateWorking, nil)
	leaf := catalogQueryTask(t, client, child, "live-child", "child", at.Add(time.Second), a2a.TaskStateWorking, &DispatchOrigin{SessionID: parent.Id, Background: true})
	catalogQueryState(t, client, root, a2a.TaskStateCompleted, at.Add(2*time.Second))
	require.NoError(t, deleteSession(t.Context(), client, parent.Id))
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", RootSessionID: parent.Id, AnchorSessionID: child.Id}, Limit: 100}
	records, err := client.ListCatalogTaskRecords(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{string(leaf.ID)}, catalogRecordIDs(records))
	require.Equal(t, string(root.ID), records.Tasks[0].Execution.Root.TaskId)
	require.Equal(t, string(root.ID), records.Tasks[0].Execution.Parent.TaskId)
	groups, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{string(leaf.ID)}, catalogGroupIDs(groups), "the visible anchor changes, never native lineage")
}

func TestCatalogEmptyAuthorizedTreeRetainsItsAnchor(t *testing.T) {
	client := catalogQueryClient(t)
	session := catalogQuerySession(t, client, "team-a", "worker", "alice", "empty", nil)
	page, err := client.ListCatalogTaskRecords(t.Context(), CatalogTaskQuery{
		Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", RootSessionID: session.Id, AnchorSessionID: session.Id}, Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, page.Tasks)
	require.Len(t, page.Sessions, 1)
	require.True(t, proto.Equal(session, page.Sessions[0]))
}
