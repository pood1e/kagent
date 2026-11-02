package grpcserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/authorization"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/executionauth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	executionsvc "github.com/kagent-dev/kagent/go/core/internal/service/execution"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/internal/substrate"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func verifyNativeRuntimePermissions(t *testing.T, api apiv1.AuthorizationServiceClient, permissions *authorization.Service, store *database.Client, kube client.Client, users auth.AuthProvider, issuer *jwttest.Issuer, admin context.Context) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, kube.Create(ctx, &v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "team", UID: "runner-1"}}))
	server := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Name: "runtime-tools", Namespace: "team", UID: "runtime-mcp-1"}, Status: v1alpha3.RemoteMCPServerStatus{DiscoveredTools: []*v1alpha3.MCPTool{{Name: "Read"}}}}
	require.NoError(t, kube.Create(ctx, server))
	workspace := &apiv1.WorkspaceAuthorizationReference{Namespace: "team", Uid: "workspace-1"}
	workspaceScope := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{Workspace: workspace}}
	ref := func(kind apiv1.AuthorizationScopeKind, name, uid string) *apiv1.AuthorizationResource {
		return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: kind, Ref: &apiv1.ResourceReference{Namespace: "team", Name: name}, Uid: uid}}}
	}
	agent := ref(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT, "runner", "runner-1")
	harness := ref(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_HARNESS, "runtime", "harness-1")
	model := ref(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION, "primary", "model-1")
	mcpServer := ref(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER, server.Name, string(server.UID))
	principalID := auth.PrincipalID("oidc:"+issuer.URL, "runtime-alice")
	subject := &apiv1.SubjectReference{Subject: &apiv1.SubjectReference_PrincipalId{PrincipalId: principalID}}
	grant := func(role apiv1.AccessRole, scope *apiv1.AuthorizationResource) *apiv1.RoleBinding {
		t.Helper()
		response, err := api.GrantRole(admin, &apiv1.GrantRoleRequest{Workspace: workspace, RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{Subject: subject, Role: role, Scope: scope}})
		require.NoError(t, err)
		return response.Binding
	}
	grant(apiv1.AccessRole_ACCESS_ROLE_WORKSPACE_MEMBER, workspaceScope)
	grant(apiv1.AccessRole_ACCESS_ROLE_EXECUTOR, workspaceScope)
	modelGrant := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, model)
	serverGrant := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, mcpServer)
	dependencies := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{
		{Resource: agent, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_INVOKE}},
		{Resource: harness, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_HARNESS_USE}},
		{Resource: model, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MODEL_CONNECTION_USE}},
		{Resource: mcpServer, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_SERVER_USE}},
	}}
	revision := database.RuntimeRevision{Revision: "runtime-fga-snapshot", Namespace: "team", AgentName: "runner", AgentUID: "runner-1", SourceSnapshot: []byte("{}"), AgentCard: &a2apb.AgentCard{Name: "runner"}, Dependencies: &apiv1.ExecutionDependencyManifest{Direct: dependencies}, ActorTemplateAtespace: "team", ActorTemplateName: "runtime-fga", ActorTemplateUID: "runtime-template-1"}
	require.NoError(t, store.UpsertAgentDefinition(ctx, database.AgentDefinition{Namespace: "team", AgentName: "runner", AgentUID: "runner-1", DesiredRevision: revision.Revision}))
	_, err := store.RecordRuntimeRevision(ctx, revision, database.RecordRevisionOptions{Ready: true})
	require.NoError(t, err)
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	keyFile := filepath.Join(t.TempDir(), "execution-key")
	require.NoError(t, os.WriteFile(keyFile, key, 0o600))
	cfg := &configv1.ExecutionAuthorizationConfig{ScopeClaim: "roles", EncryptionKeyFile: keyFile, Rules: []*configv1.ResourceScopeRule{{ResourceType: "Session", Verbs: []string{"get", "create"}}, {ResourceType: "Agent", Verbs: []string{"get", "list"}}}}
	authority, err := executionauth.NewService(store, users, cfg, false)
	require.NoError(t, err)
	actors := jwttest.New(t)
	verifier, err := substrate.NewActorJWTVerifier(ctx, actors.URL, actors.CAFile, substrate.RuntimeJWTAudience)
	require.NoError(t, err)
	normalized := &executionauth.Provider{Users: users, Authority: authority}
	provider := runtimeauth.NewProvider(normalized, verifier, store, actors.URL, authority)
	policy := runtimeauth.NewAuthorizer(&executionauth.Policy{Base: auth.NoopAuthorizer{}, Config: cfg}, store, authority)
	sessions := sessionsvc.NewService(store, policy, authorityWorkflow{store: store}, sessionsvc.WithExecutionAuthority(authority), sessionsvc.WithWorkspaceIdentity(kube), sessionsvc.WithResourceAuthorization(permissions))
	executions := executionsvc.NewService(store, sessions, authority, permissions)
	connection := newConfigurationConnection(t, kube, func(config *Config) {
		config.Authenticator, config.SessionService, config.ExecutionService = provider, sessions, executions
	})
	sessionClient := apiv1.NewSessionServiceClient(connection)
	userToken := issuer.Token(t, jwt.MapClaims{"iss": issuer.URL, "sub": "runtime-alice", "aud": "kagent-api", "exp": time.Now().Add(5 * time.Minute).Unix(), "roles": []string{"read"}, "groups": []string{}})
	user := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+userToken))
	request := &apiv1.CreateSessionRequest{Agent: &apiv1.ResourceReference{Namespace: "team", Name: "runner"}, RequestId: uuid.NewString()}
	_, err = sessionClient.CreateSession(user, request)
	assertGRPCCode(t, err, codes.PermissionDenied)
	harnessGrant := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, harness)
	created, err := sessionClient.CreateSession(user, request)
	require.NoError(t, err)
	require.Equal(t, revision.Revision, created.Session.PreparedRevision)
	require.NotNil(t, created.Session.Authority.ScopeCeiling.Resources)
	require.Len(t, created.Session.Authority.ScopeCeiling.Resources.Grants, 6)
	gatewayVerifier, err := substrate.NewGatewayActorJWTVerifier(ctx, actors.URL, actors.CAFile, "https://runtime-gateway.example")
	require.NoError(t, err)
	gatewayProvider := runtimeauth.NewProvider(normalized, gatewayVerifier, store, actors.URL, authority)
	callback := httptest.NewServer(auth.AuthnMiddleware(gatewayProvider)(http.HandlerFunc(executions.RuntimeAuthorization)))
	t.Cleanup(callback.Close)
	claims := actors.Claims("team", substrate.ActorName(created.Session.Id), "actor-uid")
	claims["aud"] = "https://runtime-gateway.example"
	actorToken := actors.Token(t, claims)
	call := func(query string, want int) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, callback.URL+"?"+query, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+actorToken)
		result, err := callback.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, result.Body.Close()) })
		require.Equal(t, want, result.StatusCode)
		return result
	}
	selected := func() {
		t.Helper()
		response := call("server=team/runtime-tools", http.StatusOK)
		var selection struct {
			Tools []string `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&selection))
		require.Equal(t, []string{"Read"}, selection.Tools)
	}
	selected()
	call("server=other-team/runtime-tools", http.StatusForbidden)
	call("workspace=team&model=primary", http.StatusOK)
	call("workspace=team&model=missing", http.StatusForbidden)
	call("model=primary", http.StatusBadRequest)
	call("workspace=other-team&model=primary", http.StatusForbidden)
	// A wildcard binding is fixed to the discovered names at Session creation.
	server.Status.DiscoveredTools = append(server.Status.DiscoveredTools, &v1alpha3.MCPTool{Name: "delete"})
	require.NoError(t, kube.Update(ctx, server))
	deleteTool := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{McpTool: &apiv1.MCPToolAuthorizationReference{Server: proto.CloneOf(mcpServer.GetKubernetes()), Name: "delete"}}}
	live, err := api.CheckPermissions(user, &apiv1.CheckPermissionsRequest{Workspace: workspace, Checks: []*apiv1.ResourceGrant{{Resource: deleteTool, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL}}}})
	require.NoError(t, err)
	require.NotEmpty(t, live.Resources[0].AllowedActions)
	retried, err := sessionClient.CreateSession(user, request)
	require.NoError(t, err)
	require.Equal(t, created.Session.Id, retried.Session.Id)
	selected()
	_, err = api.RevokeRole(admin, &apiv1.RevokeRoleRequest{Workspace: workspace, RequestId: uuid.NewString(), Binding: serverGrant})
	require.NoError(t, err)
	denied := call("server=team/runtime-tools&credential=subject_token", http.StatusForbidden)
	require.Empty(t, denied.Header.Get("Authorization"))
	readTool := proto.CloneOf(deleteTool)
	readTool.GetMcpTool().Name = "Read"
	grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, readTool)
	selected()
	exported := call("server=team/runtime-tools&credential=subject_token", http.StatusOK)
	require.Equal(t, "Bearer "+userToken, exported.Header.Get("Authorization"))
	_, err = api.RevokeRole(admin, &apiv1.RevokeRoleRequest{Workspace: workspace, RequestId: uuid.NewString(), Binding: harnessGrant})
	require.NoError(t, err)
	call("server=team/runtime-tools", http.StatusForbidden)
	call("workspace=team&model=primary", http.StatusForbidden)
	grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, harness)
	selected()
	_, err = api.RevokeRole(admin, &apiv1.RevokeRoleRequest{Workspace: workspace, RequestId: uuid.NewString(), Binding: modelGrant})
	require.NoError(t, err)
	call("workspace=team&model=primary", http.StatusForbidden)
	selected()
	// Even current workspace-wide access cannot revive an old server identity.
	grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, workspaceScope)
	require.NoError(t, kube.Delete(ctx, server))
	server.UID, server.ResourceVersion = "runtime-mcp-2", ""
	require.NoError(t, kube.Create(ctx, server))
	call("server=team/runtime-tools", http.StatusForbidden)
}
