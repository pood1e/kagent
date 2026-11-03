package authorization

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	fga "github.com/openfga/go-sdk"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func testService(t *testing.T, handler http.HandlerFunc) (*Service, *testStore) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store := new(testStore)
	service, err := NewService(Config{
		OpenFGA:    &configv1.OpenFGAConfig{ApiUrl: server.URL, StoreId: testStoreID, AuthorizationModelId: testModelID, GroupClaim: "groups"},
		Kubernetes: testResources(t), Store: store, Issuer: testIssuer,
	})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	return service, store
}

func agentPermissionRequest() *apiv1.CheckPermissionsRequest {
	return &apiv1.CheckPermissionsRequest{Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"}, Checks: []*apiv1.ResourceGrant{{
		Resource: agentReference(),
		Permissions: []apiv1.Permission{
			apiv1.Permission_PERMISSION_AGENT_READ, apiv1.Permission_PERMISSION_AGENT_INVOKE,
		},
	}}}
}

func TestChecksUsePinnedModelAndTrustedFacts(t *testing.T) {
	var calls atomic.Int32
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		require.Equal(t, "/stores/"+testStoreID+"/batch-check", request.URL.Path)
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, testModelID, body.GetAuthorizationModelId())
		require.Equal(t, fga.CONSISTENCYPREFERENCE_HIGHER_CONSISTENCY, body.GetConsistency())
		require.Len(t, body.Checks, 2)
		user, err := principalSubject("alice")
		require.NoError(t, err)
		for _, check := range body.Checks {
			require.Equal(t, user, check.TupleKey.User)
			require.Equal(t, "agent:workspace-1/agent-1", check.TupleKey.Object)
			require.Len(t, check.ContextualTuples.TupleKeys, 1)
			require.Equal(t, "workspace:workspace-1", check.ContextualTuples.TupleKeys[0].User)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, err = writer.Write([]byte("{\"result\":{\"0\":{\"allowed\":false},\"1\":{\"allowed\":true}}}"))
		require.NoError(t, err)
	})
	response, err := service.CheckPermissions(userContext(t, "alice"), agentPermissionRequest())
	require.NoError(t, err)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_INVOKE}, response.Resources[0].AllowedActions)
	require.Equal(t, "agent-1", response.Resources[0].Resource.GetKubernetes().Uid)
	require.Equal(t, int32(1), calls.Load())
}

func TestDependencyChecksUseTypedUIDBoundResources(t *testing.T) {
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Len(t, body.Checks, 3)
		for i, expected := range []struct {
			object   string
			relation string
		}{
			{"harness:workspace-1/harness-1", "can_harness_use"},
			{"model_connection:workspace-1/model-1", "can_model_connection_read"},
			{"mcp_server:workspace-1/mcp-1", "can_mcp_server_use"},
		} {
			require.Equal(t, expected.object, body.Checks[i].TupleKey.Object)
			require.Equal(t, expected.relation, body.Checks[i].TupleKey.Relation)
			require.Equal(t, "workspace:workspace-1", body.Checks[i].ContextualTuples.TupleKeys[0].User)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"result":{"0":{"allowed":true},"1":{"allowed":true},"2":{"allowed":true}}}`))
		require.NoError(t, err)
	})
	request := &apiv1.CheckPermissionsRequest{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"},
		Checks: []*apiv1.ResourceGrant{
			{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
				Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: harnessScope, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "runtime"}},
			}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_HARNESS_USE}},
			{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
				Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: modelConnectionScope, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "primary"}},
			}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MODEL_CONNECTION_READ}},
			{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
				Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: mcpServerScope, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "tools"}},
			}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_SERVER_USE}},
		},
	}
	response, err := service.CheckPermissions(userContext(t, "alice"), request)
	require.NoError(t, err)
	require.Equal(t, "harness-1", response.Resources[0].Resource.GetKubernetes().Uid)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_HARNESS_USE}, response.Resources[0].AllowedActions)
	require.Equal(t, "model-1", response.Resources[1].Resource.GetKubernetes().Uid)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_MODEL_CONNECTION_READ}, response.Resources[1].AllowedActions)
	require.Equal(t, "mcp-1", response.Resources[2].Resource.GetKubernetes().Uid)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_SERVER_USE}, response.Resources[2].AllowedActions)
}

func TestMCPToolCheckUsesServerFactAndExactToolIdentity(t *testing.T) {
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Len(t, body.Checks, 1)
		check := body.Checks[0]
		require.Equal(t, "can_mcp_tool_call", check.TupleKey.Relation)
		require.Len(t, check.ContextualTuples.TupleKeys, 2)
		require.Equal(t, "workspace:workspace-1", check.ContextualTuples.TupleKeys[0].User)
		require.Equal(t, "mcp_server:workspace-1/mcp-1", check.ContextualTuples.TupleKeys[1].User)
		require.Equal(t, check.TupleKey.Object, check.ContextualTuples.TupleKeys[1].Object)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"result":{"0":{"allowed":true}}}`))
		require.NoError(t, err)
	})
	request := &apiv1.CheckPermissionsRequest{Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"}, Checks: []*apiv1.ResourceGrant{{
		Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{
			McpTool: &apiv1.MCPToolAuthorizationReference{
				Server: &apiv1.KubernetesAuthorizationReference{Kind: mcpServerScope, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "tools"}},
				Name:   "search",
			},
		}},
		Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL},
	}}}
	response, err := service.CheckPermissions(userContext(t, "alice"), request)
	require.NoError(t, err)
	require.Equal(t, "mcp-1", response.Resources[0].Resource.GetMcpTool().Server.Uid)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL}, response.Resources[0].AllowedActions)
	request.Checks[0].Resource.GetMcpTool().Server.Uid = "old-mcp"
	response, err = service.CheckPermissions(userContext(t, "alice"), request)
	require.NoError(t, err)
	require.Empty(t, response.Resources[0].AllowedActions)
}

func TestCheckFailuresNeverGrantPartialAccess(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "unavailable", status: 503},
		{name: "missing result", body: "{\"result\":{\"0\":{\"allowed\":true}}}"},
		{name: "missing allowed", body: "{\"result\":{\"0\":{},\"1\":{\"allowed\":true}}}"},
		{name: "wrong correlation", body: "{\"result\":{\"0\":{\"allowed\":true},\"other\":{\"allowed\":true}}}"},
		{name: "evaluation error", body: "{\"result\":{\"0\":{\"allowed\":true},\"1\":{\"error\":{\"input_error\":\"validation_error\"}}}}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := testService(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if test.status != 0 {
					writer.WriteHeader(test.status)
				}
				_, err := writer.Write([]byte(test.body))
				require.NoError(t, err)
			})
			response, err := service.CheckPermissions(userContext(t, "alice"), agentPermissionRequest())
			require.Nil(t, response)
			require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable), "%v", err)
		})
	}
}

func TestMissingAndStaleResourcesReturnNoActions(t *testing.T) {
	service, _ := testService(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected FGA call") })
	for _, name := range []string{"missing", "assistant"} {
		request := agentPermissionRequest()
		request.Checks[0].Resource.GetKubernetes().Ref.Name = name
		request.Checks[0].Resource.GetKubernetes().Uid = "stale-uid"
		response, err := service.CheckPermissions(userContext(t, "alice"), request)
		require.NoError(t, err)
		require.Empty(t, response.Resources[0].AllowedActions)
		require.Equal(t, "stale-uid", response.Resources[0].Resource.GetKubernetes().Uid)
	}
}

func TestBoundIdentitiesCannotManageAuthorization(t *testing.T) {
	for _, principal := range []auth.Principal{
		{ID: "alice", RuntimeBinding: &auth.RuntimeBinding{SessionID: testSessionID}},
		{ID: "alice", Execution: &apiv1.SessionAuthority{SessionId: testSessionID}},
	} {
		service, _ := testService(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected FGA call") })
		ctx := auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(principal, nil))
		_, err := service.CheckPermissions(ctx, agentPermissionRequest())
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	}
}

func TestExecutionChecksApplyTrustedInitiatorAndImmutableResourceCeiling(t *testing.T) {
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		user, err := principalSubject("alice")
		require.NoError(t, err)
		for _, check := range body.Checks {
			require.Equal(t, user, check.TupleKey.User)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, err = writer.Write([]byte(`{"result":{"0":{"allowed":true},"1":{"allowed":true}}}`))
		require.NoError(t, err)
	})
	request := agentPermissionRequest()
	request.Workspace.Uid = "workspace-1"
	exact := agentReference()
	exact.GetKubernetes().Uid = "agent-1"
	principal := auth.Principal{ID: "alice", Execution: &apiv1.SessionAuthority{SessionId: testSessionID, InitiatorId: "alice", WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{Resources: &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{{Resource: exact, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_INVOKE}}}}}}}
	result, err := service.CheckExecutionPermissions(t.Context(), principal, request)
	require.NoError(t, err)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_INVOKE}, result.Resources[0].AllowedActions)
	principal.Execution.ScopeCeiling.Resources = &apiv1.ResourceGrantSet{}
	result, err = service.CheckExecutionPermissions(t.Context(), principal, request)
	require.NoError(t, err)
	require.Empty(t, result.Resources[0].AllowedActions, "a new live role never widens an existing execution")
	principal.Execution.ScopeCeiling.Resources = nil
	_, err = service.CheckExecutionPermissions(t.Context(), principal, request)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
}
