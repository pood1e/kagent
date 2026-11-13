package memory

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/internal/substrate"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type memoryStore struct {
	bindingErr   error
	stored       []*database.Memory
	searchResult []database.AgentMemorySearchResult
	searchLimit  int
	listResult   []database.Memory
	err          error
	deletedAgent string
	readAgent    string
}

// agentPermissions grants the configured Agent actions and records each request.
type agentPermissions struct {
	allowed  map[string][]apiv1.Permission
	requests []*apiv1.CheckPermissionsRequest
}

func (p *agentPermissions) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	p.requests = append(p.requests, request)
	response := &apiv1.CheckPermissionsResponse{}
	for _, check := range request.GetChecks() {
		ref := check.GetResource().GetKubernetes().GetRef()
		response.Resources = append(response.Resources, &apiv1.ResourceAccess{
			Resource: check.GetResource(), AllowedActions: p.allowed[ref.GetNamespace()+"/"+ref.GetName()],
		})
	}
	return response, nil
}

func readWrite(agents ...string) *agentPermissions {
	allowed := make(map[string][]apiv1.Permission, len(agents))
	for _, agent := range agents {
		allowed[agent] = []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_READ, apiv1.Permission_PERMISSION_AGENT_UPDATE}
	}
	return &agentPermissions{allowed: allowed}
}

func userContext(t *testing.T, id string) context.Context {
	t.Helper()
	return auth.AuthSessionTo(t.Context(), &authimpl.SimpleSession{P: auth.Principal{ID: id}})
}

func (store *memoryStore) StoreAgentMemories(_ context.Context, memories ...*database.Memory) error {
	if store.err != nil {
		return store.err
	}
	for _, memory := range memories {
		memory.ID = "memory-1"
	}
	store.stored = append(store.stored, memories...)
	return nil
}

func (store *memoryStore) SearchAgentMemory(_ context.Context, agentName string, _ pgvector.Vector, limit int) ([]database.AgentMemorySearchResult, error) {
	store.readAgent = agentName
	store.searchLimit = limit
	return store.searchResult, store.err
}

func (store *memoryStore) ListAgentMemories(_ context.Context, agentName string) ([]database.Memory, error) {
	store.readAgent = agentName
	return store.listResult, store.err
}

func (store *memoryStore) DeleteAgentMemory(_ context.Context, agentName string) error {
	store.deletedAgent = agentName
	return store.err
}

func TestAddDefaultsMetadataAndTTL(t *testing.T) {
	now := time.Date(2026, time.August, 3, 10, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	service := NewService(store, auth.NoopAuthorizer{}, WithClock(func() time.Time { return now }))

	id, err := service.Add(memoryContext(t), Input{
		Content: "remember this",
		Vector:  vector(VectorDimension, 0.25),
	})
	require.NoError(t, err)
	assert.Equal(t, "memory-1", id)
	require.Len(t, store.stored, 1)
	assert.Equal(t, "{}", store.stored[0].Metadata)
	assert.Equal(t, now.Add(DefaultTTLDays*24*time.Hour), *store.stored[0].ExpiresAt)
	assert.Len(t, store.stored[0].Embedding.Slice(), VectorDimension)
}

func TestAddAndBatchValidation(t *testing.T) {
	service := NewService(&memoryStore{}, auth.NoopAuthorizer{})

	_, err := service.Add(memoryContext(t), Input{Vector: vector(16, 1)})
	assert.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument))
	assert.EqualError(t, err, "vector must have exactly 768 dimensions, got 16")

	_, err = service.Add(memoryContext(t), Input{Vector: vector(VectorDimension, 1), Metadata: []byte("{")})
	assert.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument))

	_, err = service.AddBatch(memoryContext(t), nil)
	assert.EqualError(t, err, "Empty batch")

	inputs := make([]Input, MaxBatchSize+1)
	_, err = service.AddBatch(memoryContext(t), inputs)
	assert.EqualError(t, err, "batch size 51 exceeds maximum allowed size of 50")
}

func TestSearchDefaultsFiltersAndNormalizesMetadata(t *testing.T) {
	createdAt := time.Date(2026, time.August, 3, 10, 0, 0, 0, time.UTC)
	store := &memoryStore{searchResult: []database.AgentMemorySearchResult{
		{Memory: database.Memory{ID: "high", Content: "keep", Metadata: `{"source":"test"}`, CreatedAt: createdAt}, Score: 0.9},
		{Memory: database.Memory{ID: "invalid", Content: "normalize", Metadata: "{"}, Score: 0.8},
		{Memory: database.Memory{ID: "low", Content: "drop"}, Score: 0.4},
	}}
	service := NewService(store, auth.NoopAuthorizer{})

	results, err := service.Search(memoryContext(t), SearchRequest{
		Vector:   vector(VectorDimension, 0.25),
		MinScore: 0.5,
	})
	require.NoError(t, err)
	assert.Equal(t, DefaultSearchLimit, store.searchLimit)
	require.Len(t, results, 2)
	assert.Equal(t, "high", results[0].ID)
	assert.JSONEq(t, `{"source":"test"}`, string(results[0].Metadata))
	assert.JSONEq(t, `{}`, string(results[1].Metadata))
}

func TestListDeleteAndStoreErrors(t *testing.T) {
	store := &memoryStore{listResult: []database.Memory{{ID: "memory-1"}}}
	service := NewService(store, auth.NoopAuthorizer{})

	memories, err := service.List(memoryContext(t), "")
	require.NoError(t, err)
	assert.Equal(t, "memory-1", memories[0].ID)
	require.NoError(t, service.Delete(memoryContext(t), ""))
	assert.Equal(t, "team-a/agent", store.deletedAgent)

	store.err = errors.New("database unavailable")
	_, err = service.Search(memoryContext(t), SearchRequest{Vector: vector(VectorDimension, 1)})
	assert.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInternal))
	assert.ErrorContains(t, err, "database unavailable")
}

func vector(length int, value float32) []float32 {
	result := make([]float32, length)
	for index := range result {
		result[index] = value
	}
	return result
}

func (store *memoryStore) GetSessionForRuntime(_ context.Context, id, _ string) (*apiv1.Session, error) {
	if store.bindingErr != nil {
		return nil, store.bindingErr
	}
	return &apiv1.Session{Id: id, Creator: "user", Agent: &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: "agent"}}, A2AAuthority: substrate.ActorHost("team-a", substrate.ActorName(id), "")}, nil
}

type testActorVerifier struct{}

func (testActorVerifier) Verify(context.Context, string) (substrate.ActorIdentity, error) {
	return substrate.ActorIdentity{ActorName: "session-00000000-0000-0000-0000-000000000001", Atespace: "team-a", ActorUID: "actor"}, nil
}

func memoryContext(t *testing.T) context.Context {
	t.Helper()
	session, err := runtimeauth.NewAuthenticator(testActorVerifier{}).Authenticate(t.Context(), http.Header{"Authorization": {"Bearer test"}, "X-User-Id": {"forged"}, "X-Agent-Name": {"forged"}}, nil)
	require.NoError(t, err)
	require.Equal(t, auth.PrincipalID("substrate-actor", "team-a/actor"), session.Principal().ID)
	return auth.AuthSessionTo(t.Context(), session)
}

func TestActorMemoryUsesSessionAgent(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store, auth.NoopAuthorizer{})
	ctx := memoryContext(t)
	input := Input{AgentName: "foreign/agent", Content: "scoped", Vector: vector(VectorDimension, 1)}
	_, err := service.Add(ctx, input)
	require.NoError(t, err)
	_, err = service.AddBatch(ctx, []Input{input})
	require.NoError(t, err)
	require.Len(t, store.stored, 2)
	for _, memory := range store.stored {
		require.Equal(t, "team-a/agent", memory.AgentName)
		require.Equal(t, "user", memory.UserID, "the session owner is recorded as the author")
	}
	_, err = service.Search(ctx, SearchRequest{AgentName: "other/agent", Vector: input.Vector})
	require.NoError(t, err)
	require.Equal(t, "team-a/agent", store.readAgent)
}

func TestMemoryRejectsUnboundActors(t *testing.T) {
	for _, test := range []struct {
		name          string
		authenticated bool
		bindingErr    error
	}{
		{name: "missing identity"},
		{name: "deleted or replaced actor", authenticated: true, bindingErr: database.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{bindingErr: test.bindingErr}
			service := NewService(store, auth.NoopAuthorizer{})
			ctx := t.Context()
			if test.authenticated {
				ctx = memoryContext(t)
			}
			input := Input{Vector: vector(VectorDimension, 1)}
			_, err := service.Add(ctx, input)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			_, err = service.AddBatch(ctx, []Input{input})
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			_, err = service.Search(ctx, SearchRequest{Vector: input.Vector})
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			_, err = service.List(ctx, "other/agent")
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			err = service.Delete(ctx, "other/agent")
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Empty(t, store.stored)
			require.Empty(t, store.deletedAgent)
		})
	}
}

func TestUserMemoryFollowsAgentPermissions(t *testing.T) {
	store := &memoryStore{bindingErr: database.ErrNotFound}
	permissions := &agentPermissions{allowed: map[string][]apiv1.Permission{
		"team-a/agent":  {apiv1.Permission_PERMISSION_AGENT_READ, apiv1.Permission_PERMISSION_AGENT_UPDATE},
		"team-a/viewer": {apiv1.Permission_PERMISSION_AGENT_READ},
	}}
	service := NewService(store, auth.NoopAuthorizer{}, WithAgentPermissions(permissions))
	ctx := userContext(t, "owner")

	_, err := service.List(ctx, "team-a/agent")
	require.NoError(t, err)
	require.Equal(t, "team-a/agent", store.readAgent)
	require.NoError(t, service.Delete(ctx, "team-a/agent"))
	require.Equal(t, "team-a/agent", store.deletedAgent)
	read, update := permissions.requests[0], permissions.requests[1]
	require.Equal(t, "team-a", read.GetWorkspace().GetNamespace())
	require.Equal(t, apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT, read.GetChecks()[0].GetResource().GetKubernetes().GetKind())
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_READ}, read.GetChecks()[0].GetPermissions())
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_UPDATE}, update.GetChecks()[0].GetPermissions())

	store.deletedAgent = ""
	_, err = service.List(ctx, "team-a/viewer")
	require.NoError(t, err)
	require.True(t, serviceerrors.IsCode(service.Delete(ctx, "team-a/viewer"), serviceerrors.CodePermissionDenied))
	_, err = service.List(ctx, "team-b/agent")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	require.Empty(t, store.deletedAgent)

	for _, agentName := range []string{"", "agent", "team-a/", "/agent", "team-a/agent/extra"} {
		_, err := service.List(ctx, agentName)
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), agentName)
	}
	_, err = NewService(store, auth.NoopAuthorizer{}).List(ctx, "team-a/agent")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
}

type deniedMemoryAuthorizer struct{ auth.NoopAuthorizer }

func (deniedMemoryAuthorizer) Check(context.Context, auth.Principal, auth.Verb, auth.Resource) error {
	return errors.New("memory access denied")
}

func TestMemoryManagementHonorsConfiguredAuthorization(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store, deniedMemoryAuthorizer{}, WithAgentPermissions(readWrite("team-a/agent")))
	for _, ctx := range []context.Context{userContext(t, "user"), memoryContext(t)} {
		_, err := service.List(ctx, "team-a/agent")
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
		err = service.Delete(ctx, "team-a/agent")
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	}
	require.Empty(t, store.deletedAgent)
}

func TestMemorySharesNeverReachStorage(t *testing.T) {
	for _, ctx := range []context.Context{userContext(t, "owner"), memoryContext(t)} {
		store := &memoryStore{}
		service := NewService(store, auth.NoopAuthorizer{}, WithAgentPermissions(readWrite("team-a/agent")))
		shared := auth.ShareContextTo(ctx, &auth.ShareContext{SessionID: "shared-session", UserID: "owner"})
		operations := []struct {
			name string
			call func() error
		}{
			{"add", func() error {
				_, err := service.Add(shared, Input{AgentName: "team-a/agent", Vector: vector(VectorDimension, 1)})
				return err
			}},
			{"batch", func() error {
				_, err := service.AddBatch(shared, []Input{{AgentName: "team-a/agent", Vector: vector(VectorDimension, 1)}})
				return err
			}},
			{"search", func() error {
				_, err := service.Search(shared, SearchRequest{AgentName: "team-a/agent", Vector: vector(VectorDimension, 1)})
				return err
			}},
			{"list", func() error { _, err := service.List(shared, "team-a/agent"); return err }},
			{"delete", func() error { return service.Delete(shared, "team-a/agent") }},
		}
		for _, op := range operations {
			t.Run(op.name, func(t *testing.T) {
				err := op.call()
				require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied) || status.Code(err) == codes.PermissionDenied, "%v", err)
			})
		}
		require.Empty(t, store.stored)
		require.Empty(t, store.readAgent)
		require.Empty(t, store.deletedAgent)
	}
}
