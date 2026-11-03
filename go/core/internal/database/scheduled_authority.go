package database

import (
	"context"
	"fmt"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func encodeScheduledAuthority(grant *apiv1.SessionAuthority) ([]byte, error) {
	if grant.GetInitiatorId() == "" || grant.GetWorkspaceUid() == "" || grant.GetScopeCeiling().GetResources() == nil || grant.ParentSessionId != "" || grant.RootSessionId != grant.SessionId {
		return nil, ErrFailedPrecondition
	}
	if err := protovalidate.Validate(grant); err != nil {
		return nil, fmt.Errorf("validate scheduled authority: %w", err)
	}
	if err := auth.ValidateResourceCeiling(grant.ScopeCeiling.Resources); err != nil {
		return nil, err
	}
	return proto.Marshal(grant)
}

func decodeScheduledAuthority(data []byte) (*apiv1.SessionAuthority, error) {
	if len(data) == 0 {
		return nil, ErrFailedPrecondition
	}
	grant := new(apiv1.SessionAuthority)
	if err := proto.Unmarshal(data, grant); err != nil {
		return nil, err
	}
	if _, err := encodeScheduledAuthority(grant); err != nil {
		return nil, err
	}
	return grant, nil
}

// GetScheduledRunAuthority reads an owned schedule's captured execution bounds,
// including tombstones. Legacy or malformed grants fail; callers authorize use.
func (c *Client) GetScheduledRunAuthority(ctx context.Context, id uuid.UUID, creator string) (*apiv1.SessionAuthority, error) {
	data, err := queryOne(ctx, c.db, `SELECT authority FROM scheduled_run WHERE id = $1 AND creator = $2`, pgx.RowTo[[]byte], id, creator)
	if err != nil {
		return nil, notFoundOr(err)
	}
	return decodeScheduledAuthority(data)
}

// scheduledExecutionAuthority reads a firing's immutable captured grant within
// its reservation transaction and binds it to a fresh independent Session root.
func scheduledExecutionAuthority(ctx context.Context, tx pgx.Tx, execution, session uuid.UUID, creator string) (*apiv1.SessionAuthority, error) {
	data, err := queryOne(ctx, tx, `SELECT authority FROM scheduled_run_execution WHERE id = $1`, pgx.RowTo[[]byte], execution)
	if err != nil {
		return nil, err
	}
	grant, err := decodeScheduledAuthority(data)
	if err != nil {
		return nil, err
	}
	if grant.InitiatorId != creator {
		return nil, ErrFailedPrecondition
	}
	grant.SessionId, grant.RootSessionId = session.String(), session.String()
	return grant, nil
}
