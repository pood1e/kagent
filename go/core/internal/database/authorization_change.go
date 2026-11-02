package database

import (
	"bytes"
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// AuthorizationChange records one actor's immutable role-binding mutation.
// OpenFGA owns effective access; this record owns audit and request identity.
type AuthorizationChange struct {
	RequestID string
	ActorID   string
	StoreID   string
	ModelID   string
	Binding   *apiv1.RoleBinding
	Grant     bool
}

// BeginAuthorizationChange durably records an intent before external writes.
// Reusing a request ID with different content returns ErrIdempotencyConflict.
// True means the identical request was already confirmed as applied.
func (c *Client) BeginAuthorizationChange(ctx context.Context, change AuthorizationChange) (bool, error) {
	if _, err := uuid.Parse(change.RequestID); err != nil {
		return false, fmt.Errorf("invalid authorization change request ID: %w", err)
	}
	if change.ActorID == "" || change.StoreID == "" || change.ModelID == "" || change.Binding == nil {
		return false, ErrFailedPrecondition
	}
	binding, err := proto.MarshalOptions{Deterministic: true}.Marshal(change.Binding)
	if err != nil {
		return false, fmt.Errorf("encode authorization change: %w", err)
	}
	if err := execSQL(ctx, c.db, `
 INSERT INTO authorization_change (request_id, actor_id, store_id, model_id, binding, grant_access)
 VALUES ($1, $2, $3, $4, $5, $6)
 ON CONFLICT (request_id) DO NOTHING
 `, change.RequestID, change.ActorID, change.StoreID, change.ModelID, binding, change.Grant); err != nil {
		return false, err
	}
	type row struct {
		ActorID     string
		StoreID     string
		ModelID     string
		Binding     []byte
		GrantAccess bool
		Applied     bool
	}
	saved, err := queryOne(ctx, c.db, `
 SELECT actor_id, store_id, model_id, binding, grant_access, applied_at IS NOT NULL AS applied
 FROM authorization_change WHERE request_id = $1
 `, pgx.RowToStructByName[row], change.RequestID)
	if err != nil {
		return false, notFoundOr(err)
	}
	if saved.ActorID != change.ActorID || saved.StoreID != change.StoreID || saved.ModelID != change.ModelID ||
		saved.GrantAccess != change.Grant || !bytes.Equal(saved.Binding, binding) {
		return false, ErrIdempotencyConflict
	}
	return saved.Applied, nil
}

// CompleteAuthorizationChange confirms a known intent after OpenFGA committed
// its mutation marker. Missing intents return ErrNotFound; repeats preserve time.
func (c *Client) CompleteAuthorizationChange(ctx context.Context, requestID string) error {
	if _, err := uuid.Parse(requestID); err != nil {
		return fmt.Errorf("invalid authorization change request ID: %w", err)
	}
	tag, err := c.db.Exec(ctx, `
 UPDATE authorization_change SET applied_at = COALESCE(applied_at, statement_timestamp())
 WHERE request_id = $1
 `, requestID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
