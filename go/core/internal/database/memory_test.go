package database

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

func TestAgentMemoryUsesExactAgentKey(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	ctx := t.Context()
	agents := []string{"team-a/my-agent", "team-a/scheduled-agent", "team-b/my-agent", "team-a/another-agent", "team_a/my_agent"}
	for _, agent := range agents {
		require.NoError(t, client.StoreAgentMemories(ctx, &Memory{AgentName: agent, UserID: "owner", Embedding: makeEmbedding(0.5)}))
	}
	shared := &Memory{AgentName: agents[0], UserID: "teammate", Embedding: makeEmbedding(0.5)}
	require.NoError(t, client.StoreAgentMemories(ctx, shared))
	for i, agent := range agents {
		memories, err := client.ListAgentMemories(ctx, agent)
		require.NoError(t, err)
		matches, err := client.SearchAgentMemory(ctx, agent, makeEmbedding(0.5), 10)
		require.NoError(t, err)
		want := 1
		if i == 0 {
			want = 2
		}
		require.Len(t, memories, want)
		require.Len(t, matches, want)
		for _, memory := range memories {
			require.Equal(t, agent, memory.AgentName)
		}
	}
	require.NoError(t, client.DeleteAgentMemory(ctx, agents[0]))
	for i, agent := range agents {
		memories, err := client.ListAgentMemories(ctx, agent)
		require.NoError(t, err)
		if i == 0 {
			require.Empty(t, memories)
		} else {
			require.Len(t, memories, 1)
		}
	}
}

func TestDeleteAgentMemoryIsAtomic(t *testing.T) {
	db := setupTestDB(t)
	client := NewClient(db, "public")
	ctx := t.Context()
	first := &Memory{AgentName: "team/my-agent", UserID: "owner", Embedding: makeEmbedding(0.5)}
	second := &Memory{AgentName: first.AgentName, UserID: first.UserID, Embedding: makeEmbedding(0.5)}
	require.NoError(t, client.StoreAgentMemories(ctx, first, second))

	_, err := db.Exec(ctx, `CREATE TABLE memory_delete_guard (memory_id TEXT REFERENCES memory(id) ON DELETE RESTRICT)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DROP TABLE IF EXISTS memory_delete_guard`)
		require.NoError(t, err)
	})
	_, err = db.Exec(ctx, `INSERT INTO memory_delete_guard VALUES ($1)`, second.ID)
	require.NoError(t, err)
	require.Error(t, client.DeleteAgentMemory(ctx, first.AgentName))
	memories, err := client.ListAgentMemories(ctx, first.AgentName)
	require.NoError(t, err)
	require.Len(t, memories, 2)

	_, err = db.Exec(ctx, `DROP TABLE memory_delete_guard`)
	require.NoError(t, err)
	require.NoError(t, client.DeleteAgentMemory(ctx, first.AgentName))
	memories, err = client.ListAgentMemories(ctx, first.AgentName)
	require.NoError(t, err)
	require.Empty(t, memories)
}

func TestStoreAgentMemoriesCommitsIDsWithBatch(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	first := &Memory{ID: "original", AgentName: "agent", UserID: "owner", Content: "first", Embedding: makeEmbedding(0.5)}
	invalid := &Memory{AgentName: "agent", UserID: "owner", Content: "second", Embedding: pgvector.NewVector([]float32{1})}
	require.Error(t, client.StoreAgentMemories(t.Context(), first, invalid))
	require.Equal(t, "original", first.ID)
	require.Empty(t, invalid.ID)
	stored, err := client.ListAgentMemories(t.Context(), "agent")
	require.NoError(t, err)
	require.Empty(t, stored)

	invalid.Embedding = makeEmbedding(0.5)
	require.NoError(t, client.StoreAgentMemories(t.Context(), first, invalid))
	require.NotEqual(t, "original", first.ID)
	require.NotEmpty(t, invalid.ID)
	require.NotEqual(t, first.ID, invalid.ID)
	stored, err = client.ListAgentMemories(t.Context(), "agent")
	require.NoError(t, err)
	require.Len(t, stored, 2)
}
