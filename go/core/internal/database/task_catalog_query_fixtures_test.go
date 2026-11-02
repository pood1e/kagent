package database

import (
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func catalogQueryClient(t *testing.T) *Client {
	t.Helper()
	client := NewClient(setupTestDB(t), "public")
	for _, agent := range []string{"worker", "boundary"} {
		catalogQueryAgent(t, client, "team-a", agent)
	}
	catalogQueryAgent(t, client, "team-b", "worker")
	return client
}

func catalogQueryAgent(t testing.TB, client *Client, namespace, agent string) {
	t.Helper()
	revision := namespace + "-" + agent
	require.NoError(t, client.UpsertAgentDefinition(t.Context(), AgentDefinition{
		Namespace: namespace, AgentName: agent, AgentUID: agent + "-uid", DesiredRevision: revision,
	}))
	require.NoError(t, recordErr(client.RecordRuntimeRevision(t.Context(), RuntimeRevision{
		Revision: revision, Namespace: namespace, AgentName: agent, AgentUID: agent + "-uid",
		SourceSnapshot: []byte("{}"), AgentCard: &a2apb.AgentCard{}, EgressDestinations: []string{},
		ActorTemplateAtespace: namespace, ActorTemplateName: revision + "-template", ActorTemplateUID: revision + "-uid",
	}, RecordRevisionOptions{Ready: true})))
}

func catalogQuerySession(t testing.TB, client *Client, namespace, agent, owner, name string, parent *apiv1.Session) *apiv1.Session {
	t.Helper()
	request := &apiv1.Session{
		Id: uuid.NewString(), Creator: owner, Name: name,
		Agent: &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: namespace, Name: agent}},
	}
	if parent != nil {
		grant, _, err := client.GetSessionAuthority(t.Context(), parent.Id)
		require.NoError(t, err)
		request.Authority = &apiv1.SessionAuthority{
			SessionId: request.Id, InitiatorId: grant.InitiatorId, RootSessionId: grant.RootSessionId,
			ParentSessionId: parent.Id, ScopeCeiling: grant.ScopeCeiling, ExpiresAt: grant.ExpiresAt,
		}
	}
	session, _, err := client.CreateSession(t.Context(), request, uuid.NewString())
	require.NoError(t, err)
	session, err = finishSessionOperation(t.Context(), client, session.Id, apiv1.RuntimeOperation_RUNTIME_OPERATION_CREATE, "agent.example")
	require.NoError(t, err)
	return session
}

func catalogQueryTask(t testing.TB, client *Client, session *apiv1.Session, id, prompt string, at time.Time, state a2a.TaskState, origin *DispatchOrigin) *a2a.Task {
	t.Helper()
	input := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(prompt))
	task := &a2a.Task{ID: a2a.TaskID(id), ContextID: session.Id, History: []*a2a.Message{input}, Status: a2a.TaskStatus{State: a2a.TaskStateWorking, Timestamp: &at}}
	dispatch := uuid.New()
	require.NoError(t, client.ReserveSessionDispatch(t.Context(), session.Id, dispatch, input.ID, origin))
	_, err := client.CreateRuntimeTask(t.Context(), session.Id, taskMutationHash(uuid.NewString()), task, dispatch.String())
	require.NoError(t, err)
	if state != a2a.TaskStateWorking {
		catalogQueryState(t, client, task, state, at)
	}
	return task
}

func catalogQueryState(t testing.TB, client *Client, task *a2a.Task, state a2a.TaskState, at time.Time) {
	t.Helper()
	_, version, err := client.GetVersionedSessionTask(t.Context(), task.ContextID, string(task.ID))
	require.NoError(t, err)
	previous := task.Status.State
	task.Status.State, task.Status.Timestamp = state, &at
	version, err = client.UpdateSessionTask(t.Context(), task.ContextID, version, taskMutationHash(uuid.NewString()), task, task, "")
	require.NoError(t, err)
	if state.Terminal() || (previous != state && (state == a2a.TaskStateInputRequired || state == a2a.TaskStateAuthRequired)) {
		require.NoError(t, client.SettleSessionTask(t.Context(), task.ContextID, string(task.ID), version))
	}
}

func catalogRecordIDs(records CatalogRecords) []string {
	ids := make([]string, len(records.Tasks))
	for index, task := range records.Tasks {
		ids[index] = task.Task.Id
	}
	return ids
}

func catalogGroupIDs(groups CatalogGroups) []string {
	ids := make([]string, len(groups.Groups))
	for index, group := range groups.Groups {
		ids[index] = group.Root.TaskId
	}
	return ids
}
