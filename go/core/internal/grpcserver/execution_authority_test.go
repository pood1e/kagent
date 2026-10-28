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

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/dbtest"
	"github.com/kagent-dev/kagent/go/core/internal/executionauth"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	nativemcp "github.com/kagent-dev/kagent/go/core/internal/mcp"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/checkpoint"
	executionsvc "github.com/kagent-dev/kagent/go/core/internal/service/execution"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/internal/substrate"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type authorityWorkflow struct {
	*sessionsvc.ActorWorkflow
	store *database.Client
}

func (w authorityWorkflow) Create(ctx context.Context, session *apiv1.Session) (*apiv1.Session, error) {
	operation, err := w.store.BeginSessionOperation(ctx, session.Id, apiv1.RuntimeOperation_RUNTIME_OPERATION_CREATE)
	if err != nil {
		return nil, err
	}
	owner := uuid.New()
	if _, err := w.store.ClaimSessionOperation(ctx, session.Id, operation.ID, owner); err != nil {
		return nil, err
	}
	return w.store.FinishSessionOperation(ctx, session.Id, operation.ID, owner, substrate.ActorHost("team-a", substrate.ActorName(session.Id), ""), "actor-uid", "")
}

type authorityTransport struct{ token string }

func (t authorityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	cloned := r.Clone(r.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(cloned)
}

func TestExecutionAuthorityNativeAPIsAndMCP(t *testing.T) {
	dsn := dbtest.StartT(t.Context(), t)
	dbtest.MigrateT(t, dsn, false)
	db, err := database.Connect(t.Context(), &database.PostgresConfig{Pool: dbtest.PoolConfig(dsn)})
	require.NoError(t, err)
	t.Cleanup(db.Close)
	store := database.NewClient(db, "public")
	createTaskStoreSession(t, store) // Seed a ready runtime revision.
	users, actors := jwttest.New(t), jwttest.New(t)
	userProvider, err := authimpl.NewOIDCAuthenticator(t.Context(), &configv1.OIDCAuthentication{Issuer: users.URL, Audience: "kagent-api", CaFile: users.CAFile})
	require.NoError(t, err)
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	keyFile := filepath.Join(t.TempDir(), "execution-key")
	require.NoError(t, os.WriteFile(keyFile, key, 0o600))
	cfg := &configv1.ExecutionAuthorizationConfig{ScopeClaim: "roles", EncryptionKeyFile: keyFile, Rules: []*configv1.ResourceScopeRule{
		{ResourceType: "Session", Verbs: []string{"get"}},
		{ResourceType: "Session", Verbs: []string{"create", "update"}, AnyScope: []string{"write"}},
		{ResourceType: "Agent", Verbs: []string{"get", "list"}},
	}}
	authority, err := executionauth.NewService(store, userProvider, cfg, false)
	require.NoError(t, err)
	verifier, err := substrate.NewActorJWTVerifier(t.Context(), actors.URL, actors.CAFile)
	require.NoError(t, err)
	normalized := &executionauth.Provider{Users: userProvider, Authority: authority}
	provider := runtimeauth.NewProvider(normalized, verifier, store, actors.URL, authority)
	policy := runtimeauth.NewAuthorizer(&executionauth.Policy{Base: auth.NoopAuthorizer{}, Config: cfg}, store, authority)
	sessions := sessionsvc.NewService(store, policy, authorityWorkflow{store: store}, sessionsvc.WithExecutionAuthority(authority))
	executions := executionsvc.NewService(store, sessions, authority)
	connection := newConfigurationConnection(t, nil, func(config *Config) {
		config.Authenticator, config.SessionService, config.ExecutionService = provider, sessions, executions
	})
	client, executionClient := apiv1.NewSessionServiceClient(connection), apiv1.NewExecutionServiceClient(connection)
	userToken := users.Token(t, jwt.MapClaims{"iss": users.URL, "aud": "kagent-api", "sub": "alice", "roles": []string{"read", "write", "admin"}, "exp": time.Now().Add(time.Hour).Unix()})
	withToken := func(raw string) context.Context {
		return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+raw))
	}
	user := withToken(userToken)
	create := func(ctx context.Context, scopes ...string) *apiv1.Session {
		t.Helper()
		response, err := client.CreateSession(ctx, &apiv1.CreateSessionRequest{Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}, RequestId: uuid.NewString(), ScopeCeiling: &apiv1.ScopeCeiling{Scopes: scopes}})
		require.NoError(t, err)
		return response.Session
	}
	root := create(user, "read", "write")
	require.Equal(t, auth.PrincipalID("oidc:"+users.URL, "alice"), root.Creator)
	require.Equal(t, root.Id, root.Authority.RootSessionId)
	require.Equal(t, []string{"read", "write"}, root.Authority.Scopes)
	actorToken := actors.Token(t, actors.Claims("team-a", substrate.ActorName(root.Id), "actor-uid"))
	actor := withToken(actorToken)
	child := create(actor, "read", "write", "admin")
	require.Equal(t, root.Creator, child.Creator)
	require.Equal(t, root.Id, child.Authority.ParentSessionId)
	require.Equal(t, root.Id, child.Authority.RootSessionId)
	require.Equal(t, []string{"read", "write"}, child.Authority.Scopes)
	limited := create(actor, "read")
	limitedToken := actors.Token(t, actors.Claims("team-a", substrate.ActorName(limited.Id), "actor-uid"))
	_, err = client.CreateSession(withToken(limitedToken), &apiv1.CreateSessionRequest{Agent: root.Agent, RequestId: uuid.NewString()})
	assertGRPCCode(t, err, codes.PermissionDenied)
	other := create(user, "write")
	_, err = client.GetSession(actor, &apiv1.GetSessionRequest{SessionId: other.Id})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = executionClient.GetRuntimeAuthority(user, &apiv1.GetRuntimeAuthorityRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
	live, err := executionClient.GetRuntimeAuthority(actor, &apiv1.GetRuntimeAuthorityRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write"}, live.EffectiveScopes)
	_, err = executionClient.RevokeDelegationCredential(actor, &apiv1.RevokeDelegationCredentialRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = executionClient.RegisterDelegationCredential(user, &apiv1.RegisterDelegationCredentialRequest{RefreshToken: "unconfigured-refresh"})
	assertGRPCCode(t, err, codes.PermissionDenied)

	gatewayVerifier, err := substrate.NewGatewayActorJWTVerifier(t.Context(), actors.URL, actors.CAFile, "https://gateway.example")
	require.NoError(t, err)
	gatewayProvider := runtimeauth.NewProvider(normalized, gatewayVerifier, store, actors.URL, authority)
	gatewayClaims := actors.Claims("team-a", substrate.ActorName(root.Id), "actor-uid")
	gatewayClaims["aud"] = "https://gateway.example"
	gatewayToken := actors.Token(t, gatewayClaims)
	_, err = executionClient.GetRuntimeAuthority(withToken(gatewayToken), &apiv1.GetRuntimeAuthorityRequest{})
	assertGRPCCode(t, err, codes.Unauthenticated)
	callback := httptest.NewServer(auth.AuthnMiddleware(gatewayProvider)(http.HandlerFunc(executions.RuntimeAuthorization)))
	t.Cleanup(callback.Close)
	checkScope := func(raw, scope string, want int) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, callback.URL+"?scope="+scope, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+raw)
		res, err := callback.Client().Do(req)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		require.Equal(t, want, res.StatusCode)
	}
	checkScope(gatewayToken, "write", http.StatusOK)
	checkScope(gatewayToken, "admin", http.StatusForbidden)
	checkScope(actorToken, "write", http.StatusUnauthorized)

	mcpHandler, err := nativemcp.New(sessions, checkpoint.NewService(store, policy, nil, nil, authority), &struct{ a2asrv.RequestHandler }{}, nil, nil, nil)
	require.NoError(t, err)
	mcpServer := httptest.NewServer(auth.AuthnMiddleware(provider)(mcpHandler))
	t.Cleanup(mcpServer.Close)
	mcpClient, err := mcp.NewClient(&mcp.Implementation{Name: "native-authority-test", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: mcpServer.URL, HTTPClient: &http.Client{Transport: authorityTransport{actorToken}}}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mcpClient.Close()) })
	result, err := mcpClient.CallTool(t.Context(), &mcp.CallToolParams{Name: "create_session", Arguments: map[string]any{"agent": map[string]any{"namespace": "team-a", "name": "assistant"}, "request_id": uuid.NewString(), "scope_ceiling": map[string]any{"scopes": []string{"read"}}}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	created := &apiv1.CreateSessionResponse{}
	require.NoError(t, protojson.Unmarshal(raw, created))
	require.Equal(t, root.Id, created.Session.Authority.ParentSessionId)
	require.Equal(t, []string{"read"}, created.Session.Authority.Scopes)
	_, err = executionClient.RevokeSessionAuthority(actor, &apiv1.RevokeSessionAuthorityRequest{SessionId: root.Id})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = executionClient.RevokeSessionAuthority(user, &apiv1.RevokeSessionAuthorityRequest{SessionId: root.Id})
	require.NoError(t, err)
	read, err := executionClient.GetSessionAuthority(user, &apiv1.GetSessionAuthorityRequest{SessionId: child.Id})
	require.NoError(t, err)
	require.True(t, read.Revoked)
	_, err = executionClient.GetRuntimeAuthority(actor, &apiv1.GetRuntimeAuthorityRequest{})
	assertGRPCCode(t, err, codes.PermissionDenied)
	checkScope(gatewayToken, "write", http.StatusForbidden)
	independent, err := executionClient.GetSessionAuthority(user, &apiv1.GetSessionAuthorityRequest{SessionId: other.Id})
	require.NoError(t, err)
	require.False(t, independent.Revoked)
	_, err = executionClient.RevokeDelegationCredential(user, &apiv1.RevokeDelegationCredentialRequest{})
	require.NoError(t, err)
	independent, err = executionClient.GetSessionAuthority(user, &apiv1.GetSessionAuthorityRequest{SessionId: other.Id})
	require.NoError(t, err)
	require.True(t, independent.Revoked)
}
