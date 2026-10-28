package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/pkg/logging"
)

// StoreAgentMemories inserts one or more memories atomically and assigns generated IDs
// only after success. A single insert uses one statement; a batch uses a transaction.
// Creation time comes from the database. Empty input is a no-op, and failures leave
// input IDs unchanged.
func (c *Client) StoreAgentMemories(ctx context.Context, memories ...*Memory) error {
	for _, memory := range memories {
		if memory == nil {
			return fmt.Errorf("missing memory")
		}
	}
	switch len(memories) {
	case 0:
		return nil
	case 1:
		id, err := insertAgentMemory(ctx, c.db, memories[0])
		if err != nil {
			return err
		}
		memories[0].ID = id
		return nil
	}
	ids := make([]string, len(memories))
	if err := c.withTx(ctx, func(tx pgx.Tx) error {
		for i, memory := range memories {
			id, err := insertAgentMemory(ctx, tx, memory)
			if err != nil {
				return fmt.Errorf("failed to store memory: %w", err)
			}
			ids[i] = id
		}
		return nil
	}); err != nil {
		return err
	}
	for i, memory := range memories {
		memory.ID = ids[i]
	}
	return nil
}

// SearchAgentMemory returns up to limit memories for the user and agent, ranked by cosine
// similarity in one exact authorized partition. Expired rows remain searchable
// until pruned; access-count updates are
// best-effort and cannot fail a successful search.
func (c *Client) SearchAgentMemory(ctx context.Context, scope *runtimev1.MemoryScope, userID string, embedding pgvector.Vector, limit int) ([]AgentMemorySearchResult, error) {
	team, agentName, userID, err := memoryScope(scope, userID)
	if err != nil {
		return nil, err
	}
	results, err := queryMany(ctx, c.db, fmt.Sprintf(`
		SELECT id, team_namespace, COALESCE(agent_name, '') AS agent_name, COALESCE(user_id, '') AS user_id,
		    COALESCE(content, '') AS content, embedding, COALESCE(metadata, '') AS metadata,
		    COALESCE(created_at, '0001-01-01 00:00:00+00'::timestamptz) AS created_at,
		    expires_at, COALESCE(access_count, 0) AS access_count,
		    COALESCE(1 - (embedding %s $1), 0) AS score
		FROM memory
		WHERE team_namespace = $2 AND ($2 <> '' OR (agent_name = $3 AND user_id = $4))
		ORDER BY embedding %s $1 ASC
		LIMIT $5
	`, c.vectorCosineOperator, c.vectorCosineOperator), pgx.RowToStructByName[AgentMemorySearchResult], embedding, team, agentName, userID, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("failed to search agent memory: %w", err)
	}

	// Access-count bookkeeping is best-effort: a failure must not fail the search.
	if len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		// Lock in ID order to avoid deadlocks between overlapping increments.
		if err := execSQL(ctx, c.db, `
			UPDATE memory
			SET access_count = access_count + 1
			WHERE id IN (
			    SELECT id FROM memory
			    WHERE id = ANY($1::text[])
			    ORDER BY id
			    FOR UPDATE
			)
		`, ids); err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "failed to increment memory access count", "error", err)
		}
	}

	return results, nil
}

// ListAgentMemories returns memories in one authorized personal or team scope,
// ordered by descending access count. Expired rows remain visible until pruned.
func (c *Client) ListAgentMemories(ctx context.Context, scope *runtimev1.MemoryScope, userID string) ([]Memory, error) {
	team, agentName, userID, err := memoryScope(scope, userID)
	if err != nil {
		return nil, err
	}
	rows, err := queryMany(ctx, c.db, `
		SELECT id, team_namespace, COALESCE(agent_name, '') AS agent_name, COALESCE(user_id, '') AS user_id,
		    COALESCE(content, '') AS content, embedding, COALESCE(metadata, '') AS metadata,
		    COALESCE(created_at, '0001-01-01 00:00:00+00'::timestamptz) AS created_at,
		    expires_at, COALESCE(access_count, 0) AS access_count FROM memory
		WHERE team_namespace = $1 AND ($1 <> '' OR (agent_name = $2 AND user_id = $3))
		ORDER BY memory.access_count DESC
	`, pgx.RowToStructByName[Memory], team, agentName, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list agent memories: %w", err)
	}
	return rows, nil
}

// DeleteAgentMemory atomically clears one authorized personal or team scope.
// Missing rows are a no-op.
func (c *Client) DeleteAgentMemory(ctx context.Context, scope *runtimev1.MemoryScope, userID string) error {
	team, agentName, userID, err := memoryScope(scope, userID)
	if err != nil {
		return err
	}
	if err := execSQL(ctx, c.db, `
		DELETE FROM memory
		WHERE team_namespace = $1 AND ($1 <> '' OR (agent_name = $2 AND user_id = $3))
	`, team, agentName, userID); err != nil {
		return fmt.Errorf("failed to delete agent memory: %w", err)
	}
	return nil
}

// PruneExpiredMemories atomically extends expired memories with at least ten accesses by
// fifteen days and resets their access count, then deletes expired memories with fewer
// than ten accesses. Memories without an expiration remain untouched.
func (c *Client) PruneExpiredMemories(ctx context.Context) error {
	return c.withTx(ctx, func(tx pgx.Tx) error {
		if err := execSQL(ctx, tx, `
			UPDATE memory
			SET expires_at = NOW() + INTERVAL '15 days', access_count = 0
			WHERE expires_at < NOW() AND access_count >= 10
		`); err != nil {
			return fmt.Errorf("failed to extend TTL for popular memories: %w", err)
		}
		if err := execSQL(ctx, tx, `
			DELETE FROM memory
			WHERE expires_at < NOW() AND access_count < 10
		`); err != nil {
			return fmt.Errorf("failed to delete expired memories: %w", err)
		}
		return nil
	})
}

// insertAgentMemory inserts the supplied memory using the database creation time and
// returns its generated ID. It uses the caller's connection or transaction.
func insertAgentMemory(ctx context.Context, db dbExecutor, memory *Memory) (string, error) {
	return queryOne(ctx, db, `
		INSERT INTO memory (agent_name, user_id, content, embedding, metadata, created_at, expires_at, access_count, team_namespace)
		VALUES ($1, $2, $3, $4, $5, NOW(), $6, $7, $8)
		RETURNING id
	`,
		pgx.RowTo[string], &memory.AgentName, &memory.UserID, &memory.Content, memory.Embedding, &memory.Metadata,
		memory.ExpiresAt, &memory.AccessCount, memory.TeamNamespace,
	)
}

// memoryScope maps an already authorized scope to its exact storage partition.
func memoryScope(scope *runtimev1.MemoryScope, userID string) (string, string, string, error) {
	if team := scope.GetTeamNamespace(); team != "" {
		return team, "", "", nil
	}
	if scope.GetAgentName() == "" || userID == "" {
		return "", "", "", fmt.Errorf("memory scope and owner are required")
	}
	return "", scope.GetAgentName(), userID, nil
}
