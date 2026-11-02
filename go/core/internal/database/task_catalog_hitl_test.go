package database

import (
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestCatalogGroupHITLPriorityAndORFilters(t *testing.T) {
	client := catalogQueryClient(t)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	parent := catalogQuerySession(t, client, "team-a", "worker", "alice", "", nil)
	questionSession := catalogQuerySession(t, client, "team-a", "worker", "alice", "", parent)
	approvalSession := catalogQuerySession(t, client, "team-a", "worker", "alice", "", parent)
	catalogQueryTask(t, client, parent, "root", "root", at, a2a.TaskStateWorking, nil)
	question := catalogQueryTask(t, client, questionSession, "question", "question", at.Add(time.Second), a2a.TaskStateWorking, &DispatchOrigin{SessionID: parent.Id})
	approval := catalogQueryTask(t, client, approvalSession, "approval", "approval", at.Add(time.Second), a2a.TaskStateWorking, &DispatchOrigin{SessionID: parent.Id})
	question.Status.Message = a2a.NewMessage(a2a.MessageRoleAgent)
	require.NoError(t, apia2a.AttachHITL(question.Status.Message, &apiv1.AskUserRequest{
		Type: apia2a.HITLTypeAskUserRequest, Id: "ask", Questions: []*apiv1.HitlQuestion{{Question: "Which host?"}},
	}))
	catalogQueryState(t, client, question, a2a.TaskStateInputRequired, at.Add(2*time.Second))
	approval.Status.Message = a2a.NewMessage(a2a.MessageRoleAgent)
	require.NoError(t, apia2a.AttachHITL(approval.Status.Message, &apiv1.ToolApprovalRequest{
		Type: apia2a.HITLTypeToolApprovalRequest, Tools: []*apiv1.HitlTool{{Id: "tool", Name: "inspect"}},
	}))
	catalogQueryState(t, client, approval, a2a.TaskStateInputRequired, at.Add(3*time.Second))
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", WorkspaceUID: "team-a-uid", OwnerID: "alice"}, Limit: 100}
	groups, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, groups.Groups, 1)
	require.Equal(t, a2apb.TaskState_TASK_STATE_INPUT_REQUIRED, groups.Groups[0].Activity.GetState())
	require.True(t, groups.Groups[0].Activity.PendingToolApproval)
	working, input := string(a2a.TaskStateWorking), string(a2a.TaskStateInputRequired)
	no, yes := false, true
	query.Filters = []CatalogFilter{{State: &working}, {State: &input, PendingApproval: &yes}}
	filtered, err := client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, 1, filtered.Total)
	query.Filters = []CatalogFilter{{State: &input, PendingApproval: &no}}
	filtered, err = client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Zero(t, filtered.Total)
	query.Filters = nil
	activity, err := client.ListCatalogAgentActivity(t.Context(), query.Scope)
	require.NoError(t, err)
	require.Len(t, activity, 1)
	require.EqualValues(t, 3, activity[0].ActiveTasks)
	require.True(t, activity[0].Working)
	require.True(t, activity[0].PendingToolApproval)
	require.True(t, activity[0].Interrupted)
	approvalReply := a2a.NewMessage(a2a.MessageRoleUser)
	require.NoError(t, apia2a.AttachHITL(approvalReply, &apiv1.ToolApprovalResponse{
		Type: apia2a.HITLTypeToolApprovalResponse, Approvals: []*apiv1.ToolApproval{{Id: "tool", Approved: true}},
	}))
	catalogQueryReply(t, client, approval, approvalReply)
	groups, err = client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, a2apb.TaskState_TASK_STATE_INPUT_REQUIRED, groups.Groups[0].Activity.GetState())
	require.False(t, groups.Groups[0].Activity.PendingToolApproval)
	questionReply := a2a.NewMessage(a2a.MessageRoleUser)
	require.NoError(t, apia2a.AttachHITL(questionReply, &apiv1.AskUserResponse{
		Type: apia2a.HITLTypeAskUserResponse, Id: "ask", Answers: []*apiv1.AskUserAnswer{{Answer: []string{"host-a"}}},
	}))
	catalogQueryReply(t, client, question, questionReply)
	groups, err = client.ListCatalogTaskGroups(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, a2apb.TaskState_TASK_STATE_WORKING, groups.Groups[0].Activity.GetState())
}

func catalogQueryReply(t *testing.T, client *Client, task *a2a.Task, reply *a2a.Message) {
	t.Helper()
	current, version, err := client.GetVersionedSessionTask(t.Context(), task.ContextID, string(task.ID))
	require.NoError(t, err)
	reply.TaskID, reply.ContextID = task.ID, task.ContextID
	current.History = append(current.History, reply)
	dispatch := uuid.New()
	require.NoError(t, client.ReserveSessionDispatch(t.Context(), task.ContextID, dispatch, reply.ID, nil))
	_, err = client.UpdateSessionTask(t.Context(), task.ContextID, version, taskMutationHash(uuid.NewString()), current, current, dispatch.String())
	require.NoError(t, err)
}
