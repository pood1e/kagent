package grpcserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	"github.com/kagent-dev/kagent/go/api/authorization"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	memorysvc "github.com/kagent-dev/kagent/go/core/internal/service/memory"
	"github.com/kagent-dev/kagent/go/core/internal/substrate"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type memoryRPCPolicy struct {
	denied       atomic.Bool
	actorsDenied atomic.Bool
}

func (p *memoryRPCPolicy) Check(_ context.Context, principal auth.Principal, verb auth.Verb, resource auth.Resource) error {
	if principal.RuntimeBinding != nil && p.actorsDenied.Load() || principal.RuntimeBinding == nil && p.denied.Load() ||
		principal.ID == auth.PrincipalID("trusted-proxy:", "blocked") || resource.Type != "Memory" || resource.Namespace != "" {
		return errors.New("denied")
	}
	if principal.ID == auth.PrincipalID("trusted-proxy:", "viewer") && verb != auth.VerbGet && verb != auth.VerbList {
		return errors.New("read only")
	}
	return nil
}

type memoryRPCStore struct {
	mu      sync.Mutex
	rows    map[string][]database.Memory // keyed by Agent
	calls   int
	session *apiv1.Session
}

var _ memorysvc.Store = (*memoryRPCStore)(nil)

func (s *memoryRPCStore) StoreAgentMemories(_ context.Context, memories ...*database.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	for _, memory := range memories {
		memory.ID = fmt.Sprintf("memory-%d", s.calls)
		s.rows[memory.AgentName] = append(s.rows[memory.AgentName], *memory)
	}
	return nil
}
func (s *memoryRPCStore) ListAgentMemories(_ context.Context, agent string) ([]database.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return append([]database.Memory(nil), s.rows[agent]...), nil
}
func (s *memoryRPCStore) SearchAgentMemory(_ context.Context, agent string, _ pgvector.Vector, _ int) ([]database.AgentMemorySearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	var rows []database.AgentMemorySearchResult
	for _, memory := range s.rows[agent] {
		rows = append(rows, database.AgentMemorySearchResult{Memory: memory, Score: 1})
	}
	return rows, nil
}
func (s *memoryRPCStore) DeleteAgentMemory(_ context.Context, agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	delete(s.rows, agent)
	return nil
}
func (s *memoryRPCStore) rowCount(agent string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows[agent])
}

// memoryRPCPermissions admits Agents in the "team" workspace: viewers read, others also update.
type memoryRPCPermissions struct{}

func (memoryRPCPermissions) CheckPermissions(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	actions := []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_READ, apiv1.Permission_PERMISSION_AGENT_UPDATE}
	if session, ok := auth.AuthSessionFrom(ctx); ok && session.Principal().ID == auth.PrincipalID("trusted-proxy:", "viewer") {
		actions = actions[:1]
	}
	response := &apiv1.CheckPermissionsResponse{}
	for _, check := range request.GetChecks() {
		access := &apiv1.ResourceAccess{Resource: check.GetResource()}
		if request.GetWorkspace().GetNamespace() == "team" {
			access.AllowedActions = actions
		}
		response.Resources = append(response.Resources, access)
	}
	return response, nil
}

func (s *memoryRPCStore) GetSessionForRuntime(_ context.Context, id, uid string) (*apiv1.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil || s.session.Id != id || uid != "actor-uid" {
		return nil, database.ErrNotFound
	}
	return s.session, nil
}
func (s *memoryRPCStore) callCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }
func memoryVector() []float32            { v := make([]float32, memorysvc.VectorDimension); v[0] = 1; return v }

func memoryUserContext(t *testing.T, user string) context.Context {
	t.Helper()
	// The trusted proxy authenticates these claims before forwarding to Kagent.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	token := "Bearer " + header + "." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":%q}`, user))) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))
	return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", token, "x-user-id", "forged-header"))
}

func TestHumanMemorySharesTheAgentThroughGeneratedClient(t *testing.T) {
	store := &memoryRPCStore{rows: map[string][]database.Memory{
		"team/agent": {{ID: "alice-memory", Content: "alice finding"}, {ID: "bob-memory", Content: "bob finding"}},
		"team/other": {{ID: "other-agent", Content: "other Agent"}},
	}}
	policy := &memoryRPCPolicy{}
	issuer := jwttest.New(t)
	verifier, err := substrate.NewActorJWTVerifier(t.Context(), issuer.URL, issuer.CAFile, substrate.RuntimeJWTAudience)
	require.NoError(t, err)
	connection := newConfigurationConnection(t, nil, func(config *Config) {
		config.Authenticator = runtimeauth.NewProvider(authimpl.NewProxyAuthenticator("sub"), verifier, store, issuer.URL, nil)
		config.MemoryService = memorysvc.NewService(store, runtimeauth.NewAuthorizer(policy, store, nil), memorysvc.WithAgentPermissions(memoryRPCPermissions{}))
		config.ShareStore = &testShareStore{sessionShare: &apiv1.SessionShare{SessionId: testSessionID.String()}, ownerUserID: "alice"}
	})
	client := runtimev1.NewMemoryServiceClient(connection)
	caller := func(user string) context.Context {
		return memoryUserContext(t, user)
	}
	// Users see every author's memory of an Agent they may read, with no live Session.
	rows, err := client.List(caller("alice"), &runtimev1.MemoryServiceListRequest{AgentName: "team/agent"})
	require.NoError(t, err)
	require.Len(t, rows.Memories, 2)
	created, err := client.AddSession(caller("alice"), &runtimev1.MemoryServiceAddSessionRequest{ManagementAgent: "team/knowledge", Memory: &runtimev1.SessionMemoryInput{Content: "managed", Vector: memoryVector()}})
	require.NoError(t, err)
	_, err = client.AddSessionBatch(caller("alice"), &runtimev1.MemoryServiceAddSessionBatchRequest{ManagementAgent: "team/knowledge", Items: []*runtimev1.SessionMemoryInput{{Content: "keep", Vector: memoryVector()}}})
	require.NoError(t, err)
	found, err := client.Search(caller("bob"), &runtimev1.MemoryServiceSearchRequest{ManagementAgent: "team/knowledge", Vector: memoryVector()})
	require.NoError(t, err)
	require.Len(t, found.Memories, 2)
	for _, denied := range []struct{ user, agent string }{{"viewer", "team/knowledge"}, {"alice", "foreign/agent"}} {
		_, err = client.DeleteEntry(caller(denied.user), &runtimev1.MemoryServiceDeleteEntryRequest{AgentName: denied.agent, Id: created.Id})
		assertGRPCCode(t, err, codes.PermissionDenied)
	}
	_, err = client.DeleteEntry(caller("bob"), &runtimev1.MemoryServiceDeleteEntryRequest{AgentName: "team/knowledge", Id: created.Id})
	require.NoError(t, err)
	found, err = client.Search(caller("alice"), &runtimev1.MemoryServiceSearchRequest{ManagementAgent: "team/knowledge", Vector: memoryVector()})
	require.NoError(t, err)
	require.Len(t, found.Memories, 1)
	require.Equal(t, "keep", found.Memories[0].Content)
	beforeValidation := store.callCount()
	_, err = client.DeleteEntry(caller("alice"), &runtimev1.MemoryServiceDeleteEntryRequest{AgentName: "team/knowledge"})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = client.List(caller("alice"), &runtimev1.MemoryServiceListRequest{AgentName: "agent"})
	assertGRPCCode(t, err, codes.InvalidArgument)
	require.Equal(t, beforeValidation, store.callCount())
	operations := []struct {
		name string
		call func(context.Context) error
	}{
		{"add", func(ctx context.Context) error {
			_, err := client.AddSession(ctx, &runtimev1.MemoryServiceAddSessionRequest{ManagementAgent: "team/agent", Memory: &runtimev1.SessionMemoryInput{Content: "managed", Vector: memoryVector()}})
			return err
		}},
		{"search", func(ctx context.Context) error {
			_, err := client.Search(ctx, &runtimev1.MemoryServiceSearchRequest{ManagementAgent: "team/agent", Vector: memoryVector()})
			return err
		}},
		{"delete entry", func(ctx context.Context) error {
			_, err := client.DeleteEntry(ctx, &runtimev1.MemoryServiceDeleteEntryRequest{AgentName: "team/agent", Id: "alice-memory"})
			return err
		}},
		{"list", func(ctx context.Context) error {
			_, err := client.List(ctx, &runtimev1.MemoryServiceListRequest{AgentName: "team/agent"})
			return err
		}},
		{"delete", func(ctx context.Context) error {
			_, err := client.Delete(ctx, &runtimev1.MemoryServiceDeleteRequest{AgentName: "team/agent"})
			return err
		}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			before := store.callCount()
			assertGRPCCode(t, op.call(caller("blocked")), codes.PermissionDenied)
			assertGRPCCode(t, op.call(metadata.AppendToOutgoingContext(caller("alice"), "x-share-token", "share")), codes.PermissionDenied)
			require.Equal(t, before, store.callCount())
		})
	}
	_, err = client.List(caller("viewer"), &runtimev1.MemoryServiceListRequest{AgentName: "team/agent"})
	require.NoError(t, err)
	assertGRPCCode(t, operations[len(operations)-1].call(caller("viewer")), codes.PermissionDenied)
	before := store.callCount()
	_, err = client.AddSession(caller("alice"), &runtimev1.MemoryServiceAddSessionRequest{Memory: &runtimev1.SessionMemoryInput{Content: "single", Vector: memoryVector()}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = client.AddSessionBatch(caller("alice"), &runtimev1.MemoryServiceAddSessionBatchRequest{Items: []*runtimev1.SessionMemoryInput{{Vector: memoryVector()}}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = client.Search(caller("alice"), &runtimev1.MemoryServiceSearchRequest{Vector: memoryVector()})
	assertGRPCCode(t, err, codes.InvalidArgument)
	require.Equal(t, before, store.callCount())
	require.NoError(t, operations[len(operations)-1].call(caller("alice")))
	rows, err = client.List(caller("bob"), &runtimev1.MemoryServiceListRequest{AgentName: "team/agent"})
	require.NoError(t, err)
	require.Empty(t, rows.Memories, "deleting an Agent's memory removes every author's entries")
	rows, err = client.List(caller("alice"), &runtimev1.MemoryServiceListRequest{AgentName: "team/other"})
	require.NoError(t, err)
	require.Len(t, rows.Memories, 1, "deleting one Agent's memory must preserve other Agents")
	require.Equal(t, "other-agent", rows.Memories[0].Id)
}

func TestRuntimeMemoryUsesAcceptedActorBindingInsteadOfHumanClaims(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	store := &memoryRPCStore{rows: map[string][]database.Memory{
		"team/assistant": {{ID: "teammate-memory", Content: "teammate finding"}},
		"forged/agent":   {{ID: "other-agent"}},
	}, session: &apiv1.Session{Id: id, Creator: "owner", Agent: &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: "team", Name: "assistant"}, Uid: "agent-uid"}, A2AAuthority: substrate.ActorHost("team", substrate.ActorName(id), "")}}
	policy := &memoryRPCPolicy{}
	policy.denied.Store(true) // Accepted runtime authority survives later human grant removal.
	issuer := jwttest.New(t)
	verifier, err := substrate.NewActorJWTVerifier(t.Context(), issuer.URL, issuer.CAFile, substrate.RuntimeJWTAudience)
	require.NoError(t, err)
	connection := newConfigurationConnection(t, nil, func(config *Config) {
		config.Authenticator = runtimeauth.NewProvider(authimpl.NewProxyAuthenticator("sub"), verifier, store, issuer.URL, nil)
		config.MemoryService = memorysvc.NewService(store, runtimeauth.NewAuthorizer(policy, store, nil))
	})
	client := runtimev1.NewMemoryServiceClient(connection)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	for _, credential := range []string{"", "Bearer " + header + "." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iss":%q,"sub":"owner"}`, issuer.URL))) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))} {
		ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", credential))
		_, err = client.List(ctx, &runtimev1.MemoryServiceListRequest{AgentName: "team/assistant"})
		assertGRPCCode(t, err, codes.Unauthenticated)
		_, err = client.Delete(ctx, &runtimev1.MemoryServiceDeleteRequest{AgentName: "team/assistant"})
		assertGRPCCode(t, err, codes.Unauthenticated)
	}
	require.Zero(t, store.callCount())
	actor := func(identity string) context.Context {
		parts := strings.Split(identity, "/")
		token := issuer.Token(t, issuer.Claims(parts[0], parts[1], parts[2]))
		return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token, "x-user-id", "forged", "x-agent-name", "other"))
	}
	valid := "team/session-" + id + "/actor-uid"
	ctx := actor(valid)
	_, err = apiv1.NewSystemServiceClient(connection).GetVersion(ctx, &apiv1.GetVersionRequest{})
	require.NoError(t, err)
	_, err = client.AddSession(ctx, &runtimev1.MemoryServiceAddSessionRequest{Memory: &runtimev1.SessionMemoryInput{Content: "accepted", Vector: memoryVector()}})
	require.NoError(t, err)
	_, err = client.AddSessionBatch(ctx, &runtimev1.MemoryServiceAddSessionBatchRequest{Items: []*runtimev1.SessionMemoryInput{{Content: "batch", Vector: memoryVector()}}})
	require.NoError(t, err)
	found, err := client.Search(ctx, &runtimev1.MemoryServiceSearchRequest{ManagementAgent: "forged/agent", Vector: memoryVector()})
	require.NoError(t, err)
	contents := make([]string, 0, len(found.Memories))
	for _, memory := range found.Memories {
		contents = append(contents, memory.Content)
	}
	require.ElementsMatch(t, []string{"teammate finding", "accepted", "batch"}, contents, "Actors share their session Agent's memory")
	require.Equal(t, "owner", store.rows["team/assistant"][1].UserID, "the session owner is recorded as the author")

	before := store.callCount()
	_, err = client.List(ctx, &runtimev1.MemoryServiceListRequest{AgentName: "team/assistant"})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = client.Delete(ctx, &runtimev1.MemoryServiceDeleteRequest{AgentName: "team/assistant"})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = client.DeleteEntry(ctx, &runtimev1.MemoryServiceDeleteEntryRequest{AgentName: "team/assistant", Id: "teammate-memory"})
	assertGRPCCode(t, err, codes.PermissionDenied)
	require.Equal(t, before, store.callCount(), "listing and deleting shared memory are user operations")
	require.Equal(t, 3, store.rowCount("team/assistant"))
	require.Equal(t, 1, store.rowCount("forged/agent"))
	policy.actorsDenied.Store(true)
	_, err = client.Search(ctx, &runtimev1.MemoryServiceSearchRequest{Vector: memoryVector()})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = client.AddSession(ctx, &runtimev1.MemoryServiceAddSessionRequest{Memory: &runtimev1.SessionMemoryInput{Content: "revoked", Vector: memoryVector()}})
	assertGRPCCode(t, err, codes.PermissionDenied)
	require.Equal(t, before, store.callCount(), "revoked Actor policy must fail before storage")
	policy.actorsDenied.Store(false)
	_, err = client.AddSessionBatch(ctx, &runtimev1.MemoryServiceAddSessionBatchRequest{Items: []*runtimev1.SessionMemoryInput{{Content: "valid", Vector: memoryVector()}, {Content: "invalid", Vector: []float32{1}}}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = client.AddSession(ctx, &runtimev1.MemoryServiceAddSessionRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
	require.Equal(t, before, store.callCount())
	for _, identity := range []string{"other/session-" + id + "/actor-uid", "team/session-" + id + "/stale-uid", "team/session-22222222-2222-4222-8222-222222222222/actor-uid"} {
		_, err = client.Search(actor(identity), &runtimev1.MemoryServiceSearchRequest{Vector: memoryVector()})
		assertGRPCCode(t, err, codes.Unauthenticated)
		_, err = client.List(actor(identity), &runtimev1.MemoryServiceListRequest{AgentName: "team/assistant"})
		assertGRPCCode(t, err, codes.Unauthenticated)
		_, err = client.Delete(actor(identity), &runtimev1.MemoryServiceDeleteRequest{AgentName: "team/assistant"})
		assertGRPCCode(t, err, codes.Unauthenticated)
	}
	_, err = client.Search(metadata.AppendToOutgoingContext(ctx, "x-share-token", "share"), &runtimev1.MemoryServiceSearchRequest{Vector: memoryVector()})
	assertGRPCCode(t, err, codes.Unauthenticated)
	_, err = client.List(metadata.AppendToOutgoingContext(ctx, "x-share-token", "share"), &runtimev1.MemoryServiceListRequest{AgentName: "team/assistant"})
	assertGRPCCode(t, err, codes.Unauthenticated)
	_, err = client.Search(memoryUserContext(t, "owner"), &runtimev1.MemoryServiceSearchRequest{Vector: memoryVector()})
	assertGRPCCode(t, err, codes.InvalidArgument)
	require.Equal(t, before, store.callCount())
	store.mu.Lock()
	store.session = nil
	store.mu.Unlock()
	_, err = client.Search(ctx, &runtimev1.MemoryServiceSearchRequest{Vector: memoryVector()})
	assertGRPCCode(t, err, codes.Unauthenticated)
	require.Equal(t, before, store.callCount())
}

func (*memoryRPCPolicy) Scope(context.Context, auth.Principal, auth.Verb, string) (authorization.AuthorizationScope, error) {
	return authorization.AuthorizationScope{Kind: authorization.ScopeNone}, nil
}
