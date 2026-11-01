package database

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestTaskCatalogMetadataInitialPrompt(t *testing.T) {
	for _, test := range []struct {
		name     string
		messages []*a2a.Message
		want     string
	}{
		{name: "no input"},
		{name: "first empty input remains empty", messages: []*a2a.Message{
			a2a.NewMessage(a2a.MessageRoleUser), a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("later")),
		}},
		{name: "text parts retain separators", messages: []*a2a.Message{
			a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ignored")),
			a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(" first "), a2a.NewDataPart(map[string]any{"x": true}), a2a.NewTextPart("second ")),
		}, want: "first \nsecond"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := projectTaskCatalog(&a2a.Task{History: test.messages})
			require.Equal(t, test.want, got.InitialPrompt)
		})
	}
}

func TestTaskCatalogMetadataTrustedCalls(t *testing.T) {
	call := catalogCallPart(t, "same")
	response, err := apia2a.NewToolResultPart("same", "inspect", "", &apiv1.ToolResponse{
		Outcome: &apiv1.ToolResponse_Output{Output: structpb.NewStringValue("ok")},
	})
	require.NoError(t, err)
	responseOnly, err := apia2a.NewToolResultPart("response-only", "inspect", "", &apiv1.ToolResponse{
		Outcome: &apiv1.ToolResponse_Output{Output: structpb.NewStringValue("ok")},
	})
	require.NoError(t, err)
	trusted := a2a.NewMessage(a2a.MessageRoleAgent, call, response, responseOnly, catalogCallPart(t, ""))
	trusted.ID = "agent-message"
	task := &a2a.Task{
		History: []*a2a.Message{trusted, trusted},
		Status:  a2a.TaskStatus{Message: trusted},
		Artifacts: []*a2a.Artifact{{ID: "artifact", Parts: a2a.ContentParts{
			call, catalogCallPart(t, ""), catalogCallPart(t, ""),
		}}},
	}
	got := projectTaskCatalog(task)
	require.EqualValues(t, 5, got.ToolCallCount)
	task.History = append(task.History, a2a.NewMessage(a2a.MessageRoleUser, call))
	got = projectTaskCatalog(task)
	require.EqualValues(t, 4, got.ToolCallCount, "a caller-supplied ID cannot create or overwrite a call")
	task.Status.Message = a2a.NewMessage(a2a.MessageRoleAgent, catalogCallPart(t, "not-archived"))
	got = projectTaskCatalog(task)
	require.EqualValues(t, 4, got.ToolCallCount, "standalone status narration is outside taskSources")
}

func TestTaskCatalogMetadataCurrentApproval(t *testing.T) {
	message := a2a.NewMessage(a2a.MessageRoleAgent)
	require.NoError(t, apia2a.AttachHITL(message, &apiv1.ToolApprovalRequest{
		Type: apia2a.HITLTypeToolApprovalRequest, Tools: []*apiv1.HitlTool{{Id: "tool", Name: "inspect"}},
	}))
	task := &a2a.Task{Status: a2a.TaskStatus{State: a2a.TaskStateInputRequired, Message: message}}
	got := projectTaskCatalog(task)
	require.True(t, got.PendingToolApproval)
	reply := a2a.NewMessage(a2a.MessageRoleUser)
	require.NoError(t, apia2a.AttachHITL(reply, &apiv1.ToolApprovalResponse{
		Type: apia2a.HITLTypeToolApprovalResponse, Approvals: []*apiv1.ToolApproval{{Id: "tool", Approved: false}},
	}))
	task.History = append(task.History, reply)
	got = projectTaskCatalog(task)
	require.False(t, got.PendingToolApproval)
	task.History = nil
	task.Status.State = a2a.TaskStateWorking
	got = projectTaskCatalog(task)
	require.False(t, got.PendingToolApproval)
}

func catalogCallPart(t *testing.T, id string) *a2a.Part {
	t.Helper()
	part, err := apia2a.NewToolCallPart(id, "inspect", "", nil)
	require.NoError(t, err)
	return part
}

func TestTaskCatalogMetadataQuestionFacts(t *testing.T) {
	for _, test := range []struct {
		name    string
		request *apiv1.AskUserRequest
		reply   *apiv1.AskUserResponse
		want    bool
	}{
		{name: "raw input required has no question"},
		{name: "question waits", request: &apiv1.AskUserRequest{Id: "ask", Questions: []*apiv1.HitlQuestion{{Question: "Which host?"}}}, want: true},
		{name: "missing id has no question", request: &apiv1.AskUserRequest{Questions: []*apiv1.HitlQuestion{{Question: "Which host?"}}}},
		{name: "missing questions has no question", request: &apiv1.AskUserRequest{Id: "ask"}},
		{name: "matching published reply clears pending before state changes", request: &apiv1.AskUserRequest{Id: "ask", Questions: []*apiv1.HitlQuestion{{Question: "Which host?"}}}, reply: &apiv1.AskUserResponse{Id: "ask", Answers: []*apiv1.AskUserAnswer{{Answer: []string{"host-a"}}}}},
		{name: "different reply remains pending", request: &apiv1.AskUserRequest{Id: "ask", Questions: []*apiv1.HitlQuestion{{Question: "Which host?"}}}, reply: &apiv1.AskUserResponse{Id: "other", Answers: []*apiv1.AskUserAnswer{{Answer: []string{"host-a"}}}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := a2a.NewMessage(a2a.MessageRoleAgent)
			if test.request != nil {
				test.request.Type = apia2a.HITLTypeAskUserRequest
				require.NoError(t, apia2a.AttachHITL(message, test.request))
			}
			task := &a2a.Task{Status: a2a.TaskStatus{State: a2a.TaskStateInputRequired, Message: message}}
			if test.reply != nil {
				reply := a2a.NewMessage(a2a.MessageRoleUser)
				test.reply.Type = apia2a.HITLTypeAskUserResponse
				require.NoError(t, apia2a.AttachHITL(reply, test.reply))
				task.History = append(task.History, reply)
			}
			got := projectTaskCatalog(task)
			require.Equal(t, test.want, got.PendingUserInput)
			require.False(t, got.PendingToolApproval)
		})
	}
}
