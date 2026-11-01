package database

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestTaskCatalogMetadataHumanReplyBeforeWorking(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	parent := continuationSession(t, client)
	child := continuationSession(t, client, parent)
	root := continuationTask(t, client, parent, "root")
	callee := continuationTask(t, client, child, "child", &DispatchOrigin{SessionID: parent.Id})
	query := CatalogTaskQuery{Scope: CatalogScope{Namespace: "team-a", OwnerID: "alice"}, Limit: 10}
	state := func() a2apb.TaskState {
		groups, err := client.ListCatalogTaskGroups(t.Context(), query)
		require.NoError(t, err)
		require.Len(t, groups.Groups, 1)
		require.Equal(t, string(root.ID), groups.Groups[0].Root.TaskId)
		return groups.Groups[0].Activity.GetState()
	}
	settleContinuation(t, client, callee, a2a.TaskStateInputRequired)
	require.Equal(t, a2apb.TaskState_TASK_STATE_WORKING, state(), "raw input-required is not a pending HITL question")
	current, version, err := client.GetVersionedSessionTask(t.Context(), child.Id, string(callee.ID))
	require.NoError(t, err)
	question := a2a.NewMessage(a2a.MessageRoleAgent)
	require.NoError(t, apia2a.AttachHITL(question, &apiv1.AskUserRequest{
		Type: apia2a.HITLTypeAskUserRequest, Id: "ask", Questions: []*apiv1.HitlQuestion{{Question: "Which host?"}},
	}))
	current.Status.Message = question
	version, err = client.UpdateSessionTask(t.Context(), child.Id, version, taskMutationHash("question"), current, current, "")
	require.NoError(t, err)
	require.Equal(t, a2apb.TaskState_TASK_STATE_INPUT_REQUIRED, state())
	reply := a2a.NewMessage(a2a.MessageRoleUser)
	reply.TaskID, reply.ContextID = current.ID, current.ContextID
	require.NoError(t, apia2a.AttachHITL(reply, &apiv1.AskUserResponse{
		Type: apia2a.HITLTypeAskUserResponse, Id: "ask", Answers: []*apiv1.AskUserAnswer{{Answer: []string{"host-a"}}},
	}))
	current.History = append(current.History, reply)
	dispatch := uuid.New()
	require.NoError(t, client.ReserveSessionDispatch(t.Context(), child.Id, dispatch, reply.ID, nil))
	_, err = client.UpdateSessionTask(t.Context(), child.Id, version, taskMutationHash("answer before working"), current, current, dispatch.String())
	require.NoError(t, err)
	require.Equal(t, a2apb.TaskState_TASK_STATE_WORKING, state(), "a published answer clears the group priority before the child resumes")
}
