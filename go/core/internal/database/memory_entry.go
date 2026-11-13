package database

import (
	"context"
	"fmt"
)

// DeleteMemoryEntry atomically deletes one memory of the exact Agent.
// A missing or foreign entry returns ErrNotFound.
func (c *Client) DeleteMemoryEntry(ctx context.Context, agentName, id string) error {
	tag, err := c.db.Exec(ctx, `DELETE FROM memory WHERE id=$1 AND agent_name=$2`, id, agentName)
	if err != nil {
		return fmt.Errorf("delete memory entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
