package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (store *memoryStore) DeleteMemoryEntry(_ context.Context, scope *runtimev1.MemoryScope, userID, _ string) error {
	agentName := scope.GetAgentName()
	store.deletedAgent, store.deletedUser = agentName, userID
	return store.err
}

func TestManagedMemoryUsesAuthenticatedOwner(t *testing.T) {
	store := &memoryStore{bindingErr: database.ErrNotFound}
	service := NewService(store, auth.NoopAuthorizer{})
	ctx := auth.AuthSessionTo(t.Context(), &authimpl.SimpleSession{P: auth.Principal{ID: "owner"}})
	input := Input{Scope: personalMemoryScope("team-a/knowledge"), Content: "shared finding", Vector: vector(VectorDimension, 1)}
	_, err := service.Add(ctx, input)
	require.NoError(t, err)
	_, err = service.AddBatch(ctx, []Input{input})
	require.NoError(t, err)
	for _, record := range store.stored {
		require.Equal(t, "team-a/knowledge", record.AgentName)
		require.Equal(t, "owner", record.UserID)
	}
	_, err = service.Search(ctx, SearchRequest{Scope: input.Scope, Vector: vector(VectorDimension, 1)})
	require.NoError(t, err)
	require.Equal(t, input.Scope.GetAgentName(), store.readAgent)
	require.Equal(t, "owner", store.readUser)
	require.NoError(t, service.DeleteEntry(ctx, input.Scope, "selected"))
	require.Equal(t, input.Scope.GetAgentName(), store.deletedAgent)
	require.Equal(t, "owner", store.deletedUser)
	store.err = database.ErrNotFound
	require.True(t, serviceerrors.IsCode(service.DeleteEntry(ctx, input.Scope, "foreign"), serviceerrors.CodeNotFound))
}
