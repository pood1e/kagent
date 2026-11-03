package database

import (
	"context"
	"fmt"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// CheckpointExecution contains immutable inputs retained by an owned checkpoint.
// Source authority supplies its fixed bounds, independently of runtime liveness.
type CheckpointExecution struct {
	Revision  *RuntimeRevision
	Authority *apiv1.SessionAuthority
}

// GetCheckpointExecution reads an owner's READY checkpoint's pinned revision and
// inherited source ceiling. Retained bounds survive source runtime deletion;
// missing identities or malformed durable data fail. Callers authorize new use.
func (c *Client) GetCheckpointExecution(ctx context.Context, id, creator string) (*CheckpointExecution, error) {
	row, err := readCheckpoint(ctx, c.db, id, creator, new("READY"))
	if err != nil {
		return nil, notFoundOr(err)
	}
	if _, err := toSessionCheckpoint(row); err != nil {
		return nil, err
	}
	if row.PreparedRevision == nil {
		return nil, fmt.Errorf("checkpoint has no prepared revision: %w", ErrFailedPrecondition)
	}
	revision, err := c.GetRuntimeRevision(ctx, *row.PreparedRevision)
	if err != nil {
		return nil, err
	}
	authority, _, err := readSessionAuthority(ctx, c.db, row.SourceSessionID.String())
	if err != nil {
		return nil, err
	}
	return &CheckpointExecution{Revision: revision, Authority: authority}, nil
}
