package database

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type authorityRow struct {
	SessionID       uuid.UUID
	InitiatorID     string
	ParentSessionID *uuid.UUID
	RootSessionID   uuid.UUID
	Scopes          []string
	ExpiresAt       *time.Time
	Revoked         bool
}

func (a authorityRow) authority() *apiv1.SessionAuthority {
	result := &apiv1.SessionAuthority{SessionId: a.SessionID.String(), InitiatorId: a.InitiatorID, RootSessionId: a.RootSessionID.String(), ScopeCeiling: &apiv1.ScopeCeiling{Scopes: a.Scopes}}
	if a.ParentSessionID != nil {
		result.ParentSessionId = a.ParentSessionID.String()
	}
	if a.ExpiresAt != nil {
		result.ExpiresAt = timestamppb.New(*a.ExpiresAt)
	}
	return result
}

// readAuthorityChain reads the owning grant first, then every immutable ancestor.
// Callers authorize access. A broken, cyclic, or over-depth chain fails closed.
func readAuthorityChain(ctx context.Context, db dbExecutor, id string) ([]authorityRow, error) {
	rows, err := queryMany(ctx, db, `
 WITH RECURSIVE ancestors AS (
 SELECT session_id, initiator_id, parent_session_id, root_session_id, scopes, expires_at, revoked, 1 AS depth
 FROM session_authority WHERE session_id = $1
 UNION ALL
 SELECT p.session_id, p.initiator_id, p.parent_session_id, p.root_session_id, p.scopes, p.expires_at, p.revoked, a.depth + 1
 FROM session_authority p JOIN ancestors a ON p.session_id = a.parent_session_id WHERE a.depth < 32
 ) SELECT session_id, initiator_id, parent_session_id, root_session_id, scopes, expires_at, revoked FROM ancestors ORDER BY depth
 `, pgx.RowToStructByName[authorityRow], id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	root := rows[len(rows)-1]
	if root.ParentSessionID != nil || root.SessionID != rows[0].RootSessionID {
		return nil, ErrFailedPrecondition
	}
	for _, row := range rows {
		if row.InitiatorID != root.InitiatorID || row.RootSessionID != root.SessionID {
			return nil, ErrFailedPrecondition
		}
	}
	return rows, nil
}

func authorityActive(rows []authorityRow) bool {
	now := time.Now()
	for _, row := range rows {
		if row.Revoked || row.ExpiresAt != nil && !now.Before(*row.ExpiresAt) {
			return false
		}
	}
	return true
}

// GetSessionAuthority returns immutable ancestry and its inherited active state.
// Scope ceilings are intersected across the complete chain. Missing grants return
// ErrNotFound; callers must independently authorize the Session.
func (c *Client) GetSessionAuthority(ctx context.Context, id string) (*apiv1.SessionAuthority, bool, error) {
	rows, err := readAuthorityChain(ctx, c.db, id)
	if err != nil {
		return nil, false, err
	}
	result := rows[0].authority()
	for _, row := range rows[1:] {
		result.ScopeCeiling.Scopes = slices.DeleteFunc(result.ScopeCeiling.Scopes, func(scope string) bool { return !slices.Contains(row.Scopes, scope) })
		if row.ExpiresAt != nil && (result.ExpiresAt == nil || row.ExpiresAt.Before(result.ExpiresAt.AsTime())) {
			result.ExpiresAt = timestamppb.New(*row.ExpiresAt)
		}
	}
	return result, authorityActive(rows), nil
}

// lockSessionAuthority serializes grant revocation with creation and execution
// admission. Lock ordering is authority root, task root, then Session rows.
func lockSessionAuthority(ctx context.Context, tx pgx.Tx, id string) error {
	root, err := queryOne(ctx, tx, `SELECT root_session_id::text FROM session_authority WHERE session_id = $1`, pgx.RowTo[string], id)
	if err != nil {
		return notFoundOr(err)
	}
	return execSQL(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 191004))`, root)
}

func requireActiveAuthority(ctx context.Context, tx pgx.Tx, id string) error {
	rows, err := readAuthorityChain(ctx, tx, id)
	if err != nil {
		return err
	}
	if !authorityActive(rows) {
		return fmt.Errorf("execution authority revoked or expired: %w", ErrFailedPrecondition)
	}
	return nil
}

// insertSessionAuthority persists server-derived authority in the Session's
// creation transaction. Its parent must still be active and share the initiator,
// root and storage owner; child ceilings can only narrow their parent.
func insertSessionAuthority(ctx context.Context, tx pgx.Tx, session *apiv1.Session) error {
	grant := session.Authority
	if grant == nil {
		grant = &apiv1.SessionAuthority{SessionId: session.Id, InitiatorId: session.Creator, RootSessionId: session.Id, ScopeCeiling: &apiv1.ScopeCeiling{}}
	}
	if grant.ScopeCeiling == nil || grant.SessionId != session.Id || grant.InitiatorId == "" || grant.RootSessionId == "" {
		return ErrFailedPrecondition
	}
	if grant.ParentSessionId != "" {
		if err := lockSessionAuthority(ctx, tx, grant.ParentSessionId); err != nil {
			return err
		}
		chain, err := readAuthorityChain(ctx, tx, grant.ParentSessionId)
		if err != nil {
			return err
		}
		parent := chain[0]
		if !authorityActive(chain) || len(chain) >= 32 || grant.RootSessionId != parent.RootSessionID.String() || grant.InitiatorId != parent.InitiatorID {
			return ErrFailedPrecondition
		}
		if (parent.ExpiresAt == nil) != (grant.ExpiresAt == nil) ||
			parent.ExpiresAt != nil && !grant.ExpiresAt.AsTime().Equal(*parent.ExpiresAt) {
			return ErrConflict
		}
		for _, ancestor := range chain {
			if !allScopesWithin(grant.ScopeCeiling.Scopes, ancestor.Scopes) {
				return ErrFailedPrecondition
			}
			if ancestor.ExpiresAt != nil && (grant.ExpiresAt == nil || grant.ExpiresAt.AsTime().After(*ancestor.ExpiresAt)) {
				return ErrFailedPrecondition
			}
		}
		owner, err := queryOne(ctx, tx, `SELECT user_id FROM session_record WHERE id = $1`, pgx.RowTo[string], grant.ParentSessionId)
		if err != nil {
			return notFoundOr(err)
		}
		if owner != session.Creator {
			return ErrFailedPrecondition
		}
	} else if grant.RootSessionId != session.Id {
		return ErrFailedPrecondition
	}
	var expiry *time.Time
	if grant.ExpiresAt != nil {
		expiry = new(grant.ExpiresAt.AsTime())
	}
	scopes := slices.Clone(grant.ScopeCeiling.Scopes)
	if scopes == nil {
		scopes = []string{}
	}
	return execSQL(ctx, tx, `
 INSERT INTO session_authority (session_id, initiator_id, parent_session_id, root_session_id, scopes, expires_at)
 VALUES ($1, $2, NULLIF($3::text, '')::uuid, $4, $5, $6)
 `, session.Id, grant.InitiatorId, grant.ParentSessionId, grant.RootSessionId, scopes, expiry)
}

func allScopesWithin(scopes, ceiling []string) bool {
	for _, scope := range scopes {
		if !slices.Contains(ceiling, scope) {
			return false
		}
	}
	return true
}

// RevokeSessionAuthority atomically fences this subtree and queues all retained
// tasks for durable cancellation. Repeated revocations are idempotent. Callers
// authorize the owning Session; cleanup remains possible after revocation.
func (c *Client) RevokeSessionAuthority(ctx context.Context, id string) error {
	return c.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockSessionAuthority(ctx, tx, id); err != nil {
			return err
		}
		if err := execSQL(ctx, tx, `UPDATE session_authority SET revoked = TRUE WHERE session_id = $1`, id); err != nil {
			return err
		}
		return queueAuthorityCancellation(ctx, tx, id)
	})
}

func queueAuthorityCancellation(ctx context.Context, tx pgx.Tx, id string) error {
	if err := execSQL(ctx, tx, `
 WITH RECURSIVE descendants AS (
 SELECT session_id FROM session_authority WHERE session_id = $1
 UNION ALL SELECT a.session_id FROM session_authority a JOIN descendants d ON a.parent_session_id = d.session_id
 ) INSERT INTO task_cancellation (task_id, completed)
 SELECT t.id, t.state IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_REJECTED','TASK_STATE_CANCELED')
 FROM descendants d JOIN session s ON s.id = d.session_id JOIN session_task t ON t.history_id = s.history_id
 ON CONFLICT (task_id) DO NOTHING
 `, id); err != nil {
		return err
	}
	return execSQL(ctx, tx, `
 WITH RECURSIVE descendants AS (
 SELECT session_id FROM session_authority WHERE session_id = $1
 UNION ALL SELECT a.session_id FROM session_authority a JOIN descendants d ON a.parent_session_id = d.session_id
 ) UPDATE task_continuation SET abandoned = TRUE, claim_token = NULL, lease_until = NULL
 WHERE NOT delivered AND NOT abandoned AND (parent_session_id IN (SELECT session_id FROM descendants) OR child_session_id IN (SELECT session_id FROM descendants))
 `, id)
}

// requireRelatedAuthority checks a foreign dispatch inside the target root lock.
// Both immutable grants must share an active initiator/root and storage owner.
func requireRelatedAuthority(ctx context.Context, tx pgx.Tx, source, target string) error {
	sourceChain, err := readAuthorityChain(ctx, tx, source)
	if err != nil {
		return err
	}
	targetChain, err := readAuthorityChain(ctx, tx, target)
	if err != nil {
		return err
	}
	if !authorityActive(sourceChain) || !authorityActive(targetChain) || sourceChain[0].RootSessionID != targetChain[0].RootSessionID || sourceChain[0].InitiatorID != targetChain[0].InitiatorID {
		return ErrFailedPrecondition
	}
	allowed, err := queryOne(ctx, tx, `SELECT a.user_id = b.user_id FROM session_record a CROSS JOIN session_record b WHERE a.id = $1 AND b.id = $2`, pgx.RowTo[bool], source, target)
	if err != nil {
		return notFoundOr(err)
	}
	if !allowed {
		return ErrFailedPrecondition
	}
	return nil
}
