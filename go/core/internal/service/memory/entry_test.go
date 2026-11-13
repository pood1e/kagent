package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (store *memoryStore) DeleteMemoryEntry(_ context.Context, agentName, _ string) error {
	store.deletedAgent = agentName
	return store.err
}

func TestUsersShareTheAgentMemory(t *testing.T) {
	store := &memoryStore{bindingErr: database.ErrNotFound}
	service := NewService(store, auth.NoopAuthorizer{}, WithAgentPermissions(readWrite("team-a/knowledge")))
	input := Input{AgentName: "team-a/knowledge", Content: "shared finding", Vector: vector(VectorDimension, 1)}
	_, err := service.Add(userContext(t, "author"), input)
	require.NoError(t, err)
	_, err = service.AddBatch(userContext(t, "author"), []Input{input})
	require.NoError(t, err)
	for _, record := range store.stored {
		require.Equal(t, "team-a/knowledge", record.AgentName)
		require.Equal(t, "author", record.UserID)
	}
	reader := userContext(t, "teammate")
	_, err = service.Search(reader, SearchRequest{AgentName: input.AgentName, Vector: vector(VectorDimension, 1)})
	require.NoError(t, err)
	require.Equal(t, input.AgentName, store.readAgent)
	require.NoError(t, service.DeleteEntry(reader, input.AgentName, "selected"))
	require.Equal(t, input.AgentName, store.deletedAgent)
	store.err = database.ErrNotFound
	require.True(t, serviceerrors.IsCode(service.DeleteEntry(reader, input.AgentName, "foreign"), serviceerrors.CodeNotFound))
}
