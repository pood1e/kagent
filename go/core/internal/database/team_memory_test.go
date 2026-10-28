package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
)

func TestTeamMemoryPartitionsAndAtomicWrites(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	ctx := t.Context()
	team := func(namespace string) *runtimev1.MemoryScope {
		return &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_TeamNamespace{TeamNamespace: namespace}}
	}
	shared := &Memory{TeamNamespace: "team-a", Content: "shared", Embedding: makeEmbedding(.5)}
	other := &Memory{TeamNamespace: "team-b", Content: "other", Embedding: makeEmbedding(.5)}
	personal := &Memory{AgentName: "team-a/engineer", UserID: "alice", Content: "private", Embedding: makeEmbedding(.5)}
	require.NoError(t, client.StoreAgentMemories(ctx, shared, other, personal))
	for _, user := range []string{"alice", "bob", ""} {
		rows, err := client.ListAgentMemories(ctx, team("team-a"), user)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, shared.ID, rows[0].ID)
	}
	found, err := client.SearchAgentMemory(ctx, team("team-a"), "bob", makeEmbedding(.5), 5)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, shared.ID, found[0].ID)
	require.ErrorIs(t, client.DeleteMemoryEntry(ctx, team("team-b"), "", shared.ID), ErrNotFound)
	require.ErrorIs(t, client.DeleteMemoryEntry(ctx, personalMemoryScope("team-a/engineer"), "alice", shared.ID), ErrNotFound)
	invalid := &Memory{TeamNamespace: "team-a", UserID: "alice", Embedding: makeEmbedding(.5)}
	pending := &Memory{TeamNamespace: "team-a", Embedding: makeEmbedding(.5)}
	require.Error(t, client.StoreAgentMemories(ctx, pending, invalid))
	require.Empty(t, pending.ID)
	require.NoError(t, client.DeleteAgentMemory(ctx, team("team-a"), ""))
	rows, err := client.ListAgentMemories(ctx, team("team-a"), "")
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = client.ListAgentMemories(ctx, team("team-b"), "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows, err = client.ListAgentMemories(ctx, personalMemoryScope("team-a/engineer"), "alice")
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
