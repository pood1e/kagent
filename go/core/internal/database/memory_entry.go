package database

import (
	"context"
	"fmt"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
)

// DeleteMemoryEntry atomically deletes one memory in the exact agent and user scope.
// A missing or foreign entry returns ErrNotFound.
func (c *Client) DeleteMemoryEntry(ctx context.Context, scope *runtimev1.MemoryScope, userID, id string) error {
	team, agentName, userID, err := memoryScope(scope, userID)
	if err != nil {
		return err
	}
	tag, err := c.db.Exec(ctx, `DELETE FROM memory WHERE id=$1 AND team_namespace=$2 AND ($2<>'' OR (agent_name=$3 AND user_id=$4))`, id, team, agentName, userID)
	if err != nil {
		return fmt.Errorf("delete memory entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
