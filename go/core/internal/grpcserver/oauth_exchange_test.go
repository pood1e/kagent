package grpcserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/dbtest"
	"github.com/kagent-dev/kagent/go/core/internal/executionauth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	executionsvc "github.com/kagent-dev/kagent/go/core/internal/service/execution"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/internal/service/taskstore"
	"github.com/kagent-dev/kagent/go/core/internal/substrate"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func TestNativeAuthorityRFC8693RefreshRecoveryAndRevocation(t *testing.T) {
	if os.Getenv("KAGENT_TEST_AGENTGATEWAY") == "" {
		t.Skip("set KAGENT_TEST_AGENTGATEWAY to a real agentgateway binary")
	}
	dsn := dbtest.StartT(t.Context(), t)
	dbtest.MigrateT(t, dsn, false)
	db, err := database.Connect(t.Context(), &database.PostgresConfig{Pool: dbtest.PoolConfig(dsn)})
	require.NoError(t, err)
	t.Cleanup(db.Close)
	store := database.NewClient(db, "public")
	createTaskStoreSession(t, store)
	revision, err := store.GetRuntimeRevision(t.Context(), "revision-1")
	require.NoError(t, err)
	revision.Revision, revision.ActorTemplateName = "oauth-targets", "oauth-targets"
	revision.MCPTools = []v1alpha3.MCPToolBinding{{Server: corev1.TypedLocalObjectReference{Kind: "RemoteMCPServer", Name: "environment"}, Tools: []string{"read_nodes", "delete_nodes"}}}
	require.NoError(t, store.UpsertAgentDefinition(t.Context(), database.AgentDefinition{Namespace: revision.Namespace, AgentName: revision.AgentName, AgentUID: revision.AgentUID, DesiredRevision: revision.Revision}))
	require.NoError(t, store.RecordRuntimeRevision(t.Context(), *revision, database.RecordRevisionOptions{Ready: true}))
	f, actors := newAuthorityOAuthIssuer(t), jwttest.New(t)
	controlVerifier, err := substrate.NewActorJWTVerifier(t.Context(), actors.URL, actors.CAFile)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	audience := fmt.Sprintf("http://127.0.0.1:%d", port)
	gatewayVerifier, err := substrate.NewGatewayActorJWTVerifier(t.Context(), actors.URL, actors.CAFile, audience)
	require.NoError(t, err)
	cfg := f.configuration()
	authority, err := executionauth.NewService(store, f.users, cfg, false)
	require.NoError(t, err)
	normalized := &executionauth.Provider{Users: f.users, Authority: authority}
	provider := runtimeauth.NewProvider(normalized, controlVerifier, store, actors.URL, authority)
	policy := runtimeauth.NewAuthorizer(&executionauth.Policy{Base: auth.NoopAuthorizer{}, Config: cfg}, store, authority)
	sessions := sessionsvc.NewService(store, policy, authorityWorkflow{store: store}, sessionsvc.WithExecutionAuthority(authority))
	executions := executionsvc.NewService(store, sessions, authority)
	connection := newConfigurationConnection(t, nil, func(config *Config) {
		config.Authenticator, config.SessionService, config.ExecutionService = provider, sessions, executions
		config.TaskStoreService = taskstore.NewService(store, policy, nil, nil)
	})
	client, executionClient := apiv1.NewSessionServiceClient(connection), apiv1.NewExecutionServiceClient(connection)
	withToken := func(raw string) context.Context {
		return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+raw))
	}
	user := withToken(f.userToken(t, "independent-login", time.Hour))
	_, err = executionClient.RegisterDelegationCredential(user, &apiv1.RegisterDelegationCredentialRequest{RefreshToken: "initial-refresh"})
	require.NoError(t, err)
	create := func(ctx context.Context, scopes ...string) *apiv1.Session {
		t.Helper()
		response, err := client.CreateSession(ctx, &apiv1.CreateSessionRequest{Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}, RequestId: uuid.NewString(), ScopeCeiling: &apiv1.ScopeCeiling{Scopes: scopes}})
		require.NoError(t, err)
		return response.Session
	}
	root := create(user, "read", "write")
	rootActor := actors.Token(t, actors.Claims("team-a", substrate.ActorName(root.Id), "actor-uid"))
	child := create(withToken(rootActor), "read")
	require.Equal(t, root.Id, child.Authority.ParentSessionId)
	require.Nil(t, child.Authority.ExpiresAt, "a registered renewable source must outlive its first access token")
	childActor := withToken(actors.Token(t, actors.Claims("team-a", substrate.ActorName(child.Id), "actor-uid")))
	taskStore := runtimev1.NewTaskStoreServiceClient(connection)
	listTasks := func(ctx context.Context, id string) error {
		_, err := taskStore.ListTasks(ctx, &runtimev1.TaskStoreServiceListTasksRequest{SessionId: id, Request: &a2apb.ListTasksRequest{ContextId: id}})
		return err
	}
	require.NoError(t, listTasks(childActor, child.Id), "Actor persistence must be independent of initiator write scopes")
	assertGRPCCode(t, listTasks(user, child.Id), codes.PermissionDenied)
	assertGRPCCode(t, listTasks(childActor, root.Id), codes.PermissionDenied)
	claims := actors.Claims("team-a", substrate.ActorName(child.Id), "actor-uid")
	claims["aud"] = audience
	gatewayToken := actors.Token(t, claims)
	var current atomic.Value
	rebuild := func() {
		t.Helper()
		fresh, err := executionauth.NewService(store, f.users, cfg, false)
		require.NoError(t, err)
		users := &executionauth.Provider{Users: f.users, Authority: fresh}
		authn := runtimeauth.NewProvider(users, gatewayVerifier, store, actors.URL, fresh)
		grants := runtimeauth.NewAuthorizer(&executionauth.Policy{Base: auth.NoopAuthorizer{}, Config: cfg}, store, fresh)
		api := sessionsvc.NewService(store, grants, authorityWorkflow{store: store}, sessionsvc.WithExecutionAuthority(fresh))
		httpService := executionsvc.NewService(store, api, fresh)
		current.Store(auth.AuthnMiddleware(authn)(http.HandlerFunc(httpService.RuntimeAuthorization)))
	}
	rebuild()
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { current.Load().(http.Handler).ServeHTTP(w, r) }))
	t.Cleanup(callback.Close)
	gateway := startAuthorityExchangeGateway(t, f, callback.URL, port)
	sdk, err := gateway.connect(t, gatewayToken)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sdk.Close()) })
	tools, err := sdk.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	require.Equal(t, "read_nodes", tools.Tools[0].Name)
	invoke := func() {
		t.Helper()
		result, err := sdk.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_nodes", Arguments: struct{}{}})
		require.NoError(t, err)
		require.False(t, result.IsError)
	}
	invoke()
	require.Equal(t, int32(1), f.refreshes.Load())
	require.Equal(t, "renewed-1", gateway.generation.Load())
	require.GreaterOrEqual(t, f.exchanges.Load(), int32(1))
	// The IdP bearer has write scope and both tools are selected. Only the
	// child's live native ceiling may remove the selected write tool.
	rootClaims := actors.Claims("team-a", substrate.ActorName(root.Id), "actor-uid")
	rootClaims["aud"] = audience
	rootSDK, err := gateway.connect(t, actors.Token(t, rootClaims))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rootSDK.Close()) })
	rootTools, err := rootSDK.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, rootTools.Tools, 2)
	written, err := rootSDK.CallTool(t.Context(), &mcp.CallToolParams{Name: "delete_nodes", Arguments: struct{}{}})
	require.NoError(t, err)
	require.False(t, written.IsError)
	firstCalls := gateway.calls.Load()
	for _, token := range []string{"forged-token", rootActor} {
		rejected, err := gateway.connect(t, token)
		if rejected != nil {
			require.NoError(t, rejected.Close())
		}
		require.Error(t, err)
	}
	result, err := sdk.CallTool(t.Context(), &mcp.CallToolParams{Name: "delete_nodes", Arguments: struct{}{}})
	require.True(t, err != nil || result.IsError)
	require.Equal(t, firstCalls, gateway.calls.Load())
	rebuild()
	f.waitForRefresh(t)
	invoke()
	require.Equal(t, int32(2), f.refreshes.Load())
	require.Equal(t, "renewed-2", gateway.generation.Load())
	require.GreaterOrEqual(t, f.exchanges.Load(), int32(2))
	calls, exchanges := gateway.calls.Load(), f.exchanges.Load()
	_, err = executionClient.RevokeSessionAuthority(user, &apiv1.RevokeSessionAuthorityRequest{SessionId: root.Id})
	require.NoError(t, err)
	result, err = sdk.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_nodes", Arguments: struct{}{}})
	require.True(t, err != nil || result.IsError, "ancestor revocation must reject the existing MCP session, even with a cached exchange")
	require.Equal(t, calls, gateway.calls.Load())
	require.Equal(t, exchanges, f.exchanges.Load())
	require.NoError(t, listTasks(childActor, child.Id), "the bound Actor must retain its persistence cleanup boundary after revocation")
	t.Log("PASS: native durable TokenSource -> real agentgateway RFC8693; original IdP subject, distinct Actor audience, scoped environment credential, refresh after native service reconstruction, existing MCP session ancestor revocation")
}
