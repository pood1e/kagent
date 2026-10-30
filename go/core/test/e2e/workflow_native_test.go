package e2e_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	a2a "github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func TestWorkflowOrchestratesThreeNativeHarnesses(t *testing.T) {
	t.Parallel()
	target := interactionTarget(t)
	kube := interactionKubeClient(t)
	standardURL := reachableModelURL(t, startMockLLMServer(t, workflowMocks, "mocks/invoke_workflow.json"))
	standard := createInteractionTemplate(t, testHarness{name: "kagent"}, standardURL)
	codexURL := reachableModelURL(t, startMockLLMServer(t, codexInteractionMocks, "mocks/invoke_codex_builtin_tools.json"))
	codex := createCodexMockTemplate(t, codexURL)
	claudeURL := reachableServerURL(t, startMockLLMServer(t, claudeInteractionMocks, "mocks/invoke_claude_builtin_tools.json"), "")
	claude := createClaudeMockTemplate(t, claudeURL)
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "native-workflow-", Namespace: "kagent"}, Data: map[string]string{"agents.yaml": fmt.Sprintf(`
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: graph
    agent_class: Workflow
    nodes:
      - {id: standard, type: agent, agent_ref: {name: %s}}
      - {id: codex_input, type: cel, output: "'Run the requested shell command.'"}
      - {id: codex, type: agent, agent_ref: {name: %s}}
      - {id: claude_input, type: cel, output: "'Create the requested file and read it back.'"}
      - {id: claude, type: agent, agent_ref: {name: %s}}
      - {id: done, type: cel, output: "'mixed workflow completed'"}
    edges:
      - {from: START, to: standard}
      - {from: standard, to: codex_input}
      - {from: codex_input, to: codex}
      - {from: codex, to: claude_input}
      - {from: claude_input, to: claude}
      - {from: claude, to: done}
`, standard, codex, claude)}}
	require.NoError(t, kube.Create(t.Context(), configMap))
	deleteOnCleanup(t, kube, configMap)
	template := &v1alpha3.AgentTemplate{ObjectMeta: metav1.ObjectMeta{GenerateName: "native-workflow-", Namespace: "kagent"},
		Spec: v1alpha3.AgentTemplateSpec{WorkflowFrom: &v1alpha3.ConfigMapKeyReference{Name: configMap.Name, Key: "agents.yaml"}},
	}
	createAndWaitInteractionTemplateForHarness(t, kube, template, "kagent")
	fixture := newInteractionFixtureForHarnessTemplate(t, target, "kagent", template.Name)
	_, _, task := fixture.send(t, "My VPN keeps dropping")
	require.Equal(t, a2a.TaskStateCompleted, task.Status.State, taskText(task))
	require.Contains(t, taskText(task), "mixed workflow completed")

	conn := newControllerConn(t, target)
	tree, err := apiv1.NewExecutionServiceClient(conn).ListTaskExecutions(fixture.ctx, &apiv1.ListTaskExecutionsRequest{RootTaskId: string(task.ID)})
	require.NoError(t, err)
	require.Len(t, tree.Executions, 4)
	want := map[string]string{standard: "network", codex: "CODEX_BUILTIN_TOOL_DONE", claude: "CLAUDE_BUILTIN_TOOLS_DONE"}
	for _, execution := range tree.Executions {
		if execution.Relation == apiv1.TaskRelation_TASK_RELATION_ROOT {
			continue
		}
		require.Equal(t, apiv1.TaskRelation_TASK_RELATION_DELEGATION, execution.Relation)
		require.False(t, execution.Background)
		require.Equal(t, string(task.ID), execution.Parent.TaskId)
		child, err := fixture.sessions.GetSession(fixture.ctx, &apiv1.GetSessionRequest{SessionId: execution.Task.SessionId})
		require.NoError(t, err)
		session := child.Session
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(context.Background(), "x-user-id", "e2e"), time.Minute)
			defer cancel()
			require.NoError(t, deleteIdleSession(ctx, fixture.sessions, session.Id))
		})
		require.NotEmpty(t, session.PreparedRevision)
		require.Equal(t, fixture.sessionID, session.Authority.ParentSessionId)
		marker, ok := want[session.Agent.Name]
		require.True(t, ok, "unexpected child %s", session.Agent.Name)
		delete(want, session.Agent.Name)
		stored, err := fixture.client.GetTask(fixture.ctx, &a2apb.GetTaskRequest{Tenant: "kagent/" + session.Agent.Name, Id: execution.Task.TaskId})
		require.NoError(t, err)
		require.Equal(t, a2apb.TaskState_TASK_STATE_COMPLETED, stored.Status.State)
		require.Contains(t, stored.String(), marker)
	}
	require.Empty(t, want)
}
