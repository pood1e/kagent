package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"

	"github.com/kagent-dev/kagent/go/adk/pkg/a2a"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
)

// rcaWorkflow names its model by ModelConfig and its MCP server by the
// RemoteMCPServer of a binding, which is all the kagent runtime offers.
const rcaWorkflow = `{"apiVersion":"adk.declarative/v0.2","kind":"AgentSystem","agents":[{"name":"rca","agent_class":"Workflow","entry":"diagnose",
"nodes":[
  {"id":"diagnose","type":"llm","model":"diagnosis","instruction":"Diagnose the incident."},
  {"id":"args","type":"cel","output":"{'summary': input}"},
  {"id":"record","type":"tool","server":"incidents","tool":"record_analysis"},
  {"id":"done","type":"cel","output":"'recorded ' + input.output.id"}],
"edges":[{"from":"diagnose","to":"args"},{"from":"args","to":"record"},{"from":"record","to":"done"}]}]}`

func TestCreateGoogleADKAgentRunsDeclarativeWorkflow(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "diagnosis-model" {
			t.Errorf("model request for %q: %v", req.Model, err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"x","object":"chat.completion","model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"link down on leaf-1"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(models.Close)

	recorded := make(chan map[string]any, 1)
	incidents := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "incidents", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(incidents, &mcpsdk.Tool{Name: "record_analysis"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, args map[string]any) (*mcpsdk.CallToolResult, map[string]any, error) {
		recorded <- args
		return nil, map[string]any{"id": "analysis-1"}, nil
	})
	tools := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return incidents }, nil))
	t.Cleanup(func() {
		// The runtime keeps its MCP sessions, including the standalone SSE
		// stream, for the life of the process.
		tools.CloseClientConnections()
		tools.Close()
	})

	sum := sha256.Sum256([]byte(rcaWorkflow))
	config := &configv1.ADKConfig{Agent: &configv1.ADKAgentConfig{
		Model: openAIModel(models.URL, "agent-model"), Stream: new(false),
		HttpTools: []*configv1.ADKHTTPMCPServerConfig{{
			Name: "incidents", Params: &configv1.ADKHTTPConnectionParams{Url: tools.URL}, Tools: []string{"record_analysis"},
		}},
		Workflow: &configv1.ADKWorkflowConfig{
			Document: []byte(rcaWorkflow), Sha256: hex.EncodeToString(sum[:]),
			Models: map[string]*configv1.ADKModelConfig{"diagnosis": openAIModel(models.URL, "diagnosis-model")},
		},
	}}

	root, err := CreateGoogleADKAgent(t.Context(), config, "rca", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "rca", root.Name())

	require.Equal(t, []string{"link down on leaf-1", "recorded analysis-1"}, runWorkflow(t, root, "incident 42"),
		"the model's answer and the terminal node's output reach the client; the tool call stays data")
	require.Equal(t, map[string]any{"summary": "link down on leaf-1"}, <-recorded)
}

func TestCreateGoogleADKAgentRejectsUnknownWorkflowNames(t *testing.T) {
	sum := sha256.Sum256([]byte(rcaWorkflow))
	config := &configv1.ADKConfig{Agent: &configv1.ADKAgentConfig{
		Model:    openAIModel("http://127.0.0.1:1", "agent-model"),
		Workflow: &configv1.ADKWorkflowConfig{Document: []byte(rcaWorkflow), Sha256: hex.EncodeToString(sum[:])},
	}}
	_, err := CreateGoogleADKAgent(t.Context(), config, "rca", nil, nil)
	require.ErrorContains(t, err, `model "diagnosis" is not registered`)
	require.ErrorContains(t, err, `server "incidents" is not declared in mcp_servers or registered`)
}

func TestWorkflowLLMUsesTheTemplateModelAndTools(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "agent-model" {
			t.Errorf("model request for %q: %v", req.Model, err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		message := `{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"record_analysis","arguments":"{\"summary\":\"fan failure\"}"}}]}`
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			message = `{"role":"assistant","content":"recorded"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"x","object":"chat.completion","model":"x","choices":[{"index":0,"message":%s,"finish_reason":"stop"}]}`, message)
	}))
	t.Cleanup(models.Close)

	recorded := make(chan map[string]any, 1)
	incidents := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "incidents", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(incidents, &mcpsdk.Tool{Name: "record_analysis"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, args map[string]any) (*mcpsdk.CallToolResult, map[string]any, error) {
		recorded <- args
		return nil, map[string]any{"id": "analysis-1"}, nil
	})
	tools := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return incidents }, nil))
	t.Cleanup(func() {
		tools.CloseClientConnections()
		tools.Close()
	})

	const document = `{"apiVersion":"adk.declarative/v0.2","kind":"AgentSystem","agents":[{"name":"analyst","agent_class":"LlmAgent","model":"template:model","instruction":"Record the analysis.","tools":["template:tools"]}]}`
	sum := sha256.Sum256([]byte(document))
	config := &configv1.ADKConfig{Agent: &configv1.ADKAgentConfig{
		Model: openAIModel(models.URL, "agent-model"), Stream: new(false),
		HttpTools: []*configv1.ADKHTTPMCPServerConfig{{
			Name: "incidents", Params: &configv1.ADKHTTPConnectionParams{Url: tools.URL}, Tools: []string{"record_analysis"},
		}},
		Workflow: &configv1.ADKWorkflowConfig{
			Document: []byte(document), Sha256: hex.EncodeToString(sum[:]),
			Models: map[string]*configv1.ADKModelConfig{"template:model": openAIModel(models.URL, "agent-model")},
		},
	}}

	root, err := CreateGoogleADKAgent(t.Context(), config, "analyst", nil, nil)
	require.NoError(t, err)
	require.Contains(t, runWorkflow(t, root, "incident 42"), "recorded")
	require.Equal(t, map[string]any{"summary": "fan failure"}, <-recorded)
}

// runWorkflow runs one A2A task through the runtime's executor and returns the
// text of its artifacts once the task completes.
func runWorkflow(t *testing.T, root adkagent.Agent, input string) []string {
	t.Helper()
	sessions := adksession.InMemoryService()
	executor, err := a2a.NewKAgentExecutor(a2a.KAgentExecutorConfig{
		AppName: "test", SessionService: sessions, Logger: slog.New(slog.DiscardHandler),
		RunnerConfig: runner.Config{AppName: "test", Agent: root},
	})
	require.NoError(t, err)
	request := &a2asrv.ExecutorContext{
		TaskID: "task-1", ContextID: "context-1",
		Message: a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart(input)),
	}
	var texts []string
	var state a2atype.TaskState
	for event, err := range executor.Execute(t.Context(), request) {
		require.NoError(t, err)
		switch event := event.(type) {
		case *a2atype.TaskArtifactUpdateEvent:
			for _, part := range event.Artifact.Parts {
				if text := part.Text(); text != "" {
					texts = append(texts, text)
				}
			}
		case *a2atype.TaskStatusUpdateEvent:
			state = event.Status.State
		}
	}
	require.Equal(t, a2atype.TaskStateCompleted, state)
	return texts
}
