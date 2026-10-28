package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kagent-dev/kagent/go/api/authorization"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	memoryservice "github.com/kagent-dev/kagent/go/core/internal/service/memory"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type collaborationStore struct {
	*fakeSessionStore
	memoryservice.Store
	created                *apiv1.Session
	requestID, team, owner string
	queries                []string
}

func (s *collaborationStore) CreateSession(_ context.Context, session *apiv1.Session, requestID string) (*apiv1.Session, bool, error) {
	s.created, s.requestID = session, requestID
	return session, true, nil
}
func (s *collaborationStore) SearchAgentMemory(_ context.Context, scope *runtimev1.MemoryScope, owner string, _ pgvector.Vector, _ int) ([]database.AgentMemorySearchResult, error) {
	s.team, s.owner = scope.GetTeamNamespace(), owner
	return []database.AgentMemorySearchResult{{Memory: database.Memory{ID: "memory", Content: "native team knowledge", Metadata: `{"source":"manual"}`}, Score: .9}}, nil
}
func (s *collaborationStore) Generate(_ context.Context, texts []string) ([][]float32, error) {
	s.queries = texts
	vector := make([]float32, memoryservice.VectorDimension)
	vector[0] = 1
	return [][]float32{vector}, nil
}

func TestNativeCollaborationThroughSDKClient(t *testing.T) {
	store := &collaborationStore{fakeSessionStore: &fakeSessionStore{}}
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "assistant"}},
		&v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "hidden"}},
	).Build()
	policy := &templateListAuthorizer{scope: authorization.AuthorizationScope{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{All: []authorization.ScopePredicate{
		{Attribute: authorization.AttributeNamespace, Operator: authorization.ScopeIn, Values: []string{"team-a"}},
	}}}}}
	catalog := kubecrud.NewService(kube, policy, &v1alpha3.Agent{}, &v1alpha3.AgentList{}, "Agent")
	gateway := &fakeGateway{completeOnDrain: true}
	handler, err := New(sessionsvc.NewService(store, auth.NoopAuthorizer{}, &fakeSessionWorkflow{}), testCheckpointService(), gateway, nil, nil,
		&Collaboration{Agents: catalog, Memory: memoryservice.NewService(store, auth.NoopAuthorizer{}, memoryservice.WithEmbedder(store))})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(auth.AuthSessionTo(r.Context(), testSession{})))
	}))
	t.Cleanup(server.Close)
	client, err := mcp.NewClient(&mcp.Implementation{Name: "native-collaboration", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	listed, err := client.ListTools(t.Context(), nil)
	require.NoError(t, err)
	for _, name := range []string{"create_session", "invoke_session", "list_agents", "search_memory"} {
		found := false
		for _, tool := range listed.Tools {
			if tool.Name == name {
				found = true
				require.NotNil(t, tool.InputSchema)
				require.NotNil(t, tool.OutputSchema)
			}
		}
		require.True(t, found, name)
	}
	call := func(name string, arguments map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
		require.NoError(t, err)
		if !result.IsError {
			for _, tool := range listed.Tools {
				if tool.Name != name {
					continue
				}
				raw, err := json.Marshal(tool.OutputSchema)
				require.NoError(t, err)
				var schema jsonschema.Schema
				require.NoError(t, json.Unmarshal(raw, &schema))
				resolved, err := schema.Resolve(nil)
				require.NoError(t, err)
				require.NoError(t, resolved.Validate(result.StructuredContent))
			}
		}
		return result
	}
	created := call("create_session", map[string]any{"agent": map[string]any{"namespace": "team-a", "name": "assistant"}, "request_id": "stable-request", "name": "Delegated task"})
	require.False(t, created.IsError)
	require.Equal(t, "stable-request", store.requestID)
	require.Equal(t, "Delegated task", store.created.Name)
	require.NotEmpty(t, store.created.Id)
	require.True(t, call("create_session", map[string]any{"agent": map[string]any{"namespace": "team-a", "name": "assistant"}}).IsError)
	result := call("list_agents", map[string]any{})
	require.False(t, result.IsError)
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	require.Contains(t, string(raw), "assistant")
	require.NotContains(t, string(raw), "hidden")
	require.Equal(t, "Agent", policy.resourceType)
	result = call("search_memory", map[string]any{"scope": map[string]any{"team_namespace": "team-a"}, "query": "deployment runbook"})
	require.False(t, result.IsError)
	require.Equal(t, "team-a", store.team)
	require.Empty(t, store.owner)
	require.Equal(t, []string{"deployment runbook"}, store.queries)
	raw, err = json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	require.Contains(t, string(raw), "native team knowledge")
	require.Contains(t, string(raw), "manual")
	require.True(t, call("search_memory", map[string]any{"query": ""}).IsError)
	result = call("invoke_session", map[string]any{"session_id": testSessionID, "message": "background work", "message_id": "stable-message", "background": true})
	require.False(t, result.IsError)
	require.Equal(t, testTaskID, result.Content[0].(*mcp.TextContent).Text)
	require.Equal(t, a2atype.TaskStateWorking, gateway.task.Status.State, "the observer must return before draining background work")
	require.True(t, call("invoke_session", map[string]any{"session_id": testSessionID, "message": "background work", "background": true}).IsError)
}
