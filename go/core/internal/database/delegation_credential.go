package database

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DelegationCredential contains ciphertext only. The vault owns encryption,
// identity validation and OAuth2; the store owns refresh fencing and durability.
type DelegationCredential struct {
	Ciphertext []byte
	Version    int64
}

// GetDelegationCredential reads one principal's credential or ErrNotFound.
func (c *Client) GetDelegationCredential(ctx context.Context, principal string) (*DelegationCredential, error) {
	row, err := queryOne(ctx, c.db, `SELECT ciphertext, version FROM delegation_credential WHERE principal_id = $1`, pgx.RowToAddrOfStructByName[DelegationCredential], principal)
	return row, notFoundOr(err)
}

// SaveDelegationCredential registers a renewable credential and removes the
// prior access-token deadline from nonrevoked grants in one transaction.
func (c *Client) SaveDelegationCredential(ctx context.Context, principal string, ciphertext []byte) error {
	return c.withTx(ctx, func(tx pgx.Tx) error {
		if err := execSQL(ctx, tx, `
 INSERT INTO delegation_credential (principal_id, ciphertext) VALUES ($1, $2)
 ON CONFLICT (principal_id) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, version = delegation_credential.version + 1, refresh_lease = NULL, refresh_expires_at = NULL
 `, principal, ciphertext); err != nil {
			return err
		}
		roots, err := lockInitiatorRoots(ctx, tx, principal)
		if err != nil || len(roots) == 0 {
			return err
		}
		return execSQL(ctx, tx, `
 UPDATE session_authority SET expires_at = NULL
 WHERE initiator_id = $1 AND expires_at IS NOT NULL AND NOT revoked
   AND root_session_id IN (SELECT session_id FROM session_authority WHERE initiator_id = $1 AND parent_session_id IS NULL AND NOT revoked)
 `, principal)
	})
}

// ClaimDelegationCredential leases a single version for a bounded refresh.
// False means another refresh or credential replacement won; no lock survives.
func (c *Client) ClaimDelegationCredential(ctx context.Context, principal string, version int64, lease uuid.UUID) (bool, error) {
	tag, err := c.db.Exec(ctx, `
 UPDATE delegation_credential SET refresh_lease = $3, refresh_expires_at = clock_timestamp() + INTERVAL '30 seconds'
 WHERE principal_id = $1 AND version = $2 AND (refresh_lease IS NULL OR refresh_expires_at < clock_timestamp())
 `, principal, version, lease)
	return tag.RowsAffected() == 1, err
}

// FinishDelegationCredential commits a rotated credential only for the current
// unexpired lease and version. Lost leases cannot overwrite newer credentials.
func (c *Client) FinishDelegationCredential(ctx context.Context, principal string, version int64, lease uuid.UUID, ciphertext []byte) (bool, error) {
	tag, err := c.db.Exec(ctx, `
 UPDATE delegation_credential SET ciphertext = $4, version = version + 1, refresh_lease = NULL, refresh_expires_at = NULL
 WHERE principal_id = $1 AND version = $2 AND refresh_lease = $3 AND refresh_expires_at > clock_timestamp()
 `, principal, version, lease, ciphertext)
	return tag.RowsAffected() == 1, err
}

// InvalidateDelegationCredential removes only the failed refresh's version,
// preserving a newer credential registered concurrently.
func (c *Client) InvalidateDelegationCredential(ctx context.Context, principal string, version int64, lease uuid.UUID) error {
	return c.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM delegation_credential WHERE principal_id = $1 AND version = $2 AND refresh_lease = $3`, principal, version, lease)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return revokeInitiatorAuthority(ctx, tx, principal)
	})
}

// DeleteDelegationCredential atomically removes the source, revokes its current
// execution roots and queues their durable cancellation.
func (c *Client) DeleteDelegationCredential(ctx context.Context, principal string) error {
	return c.withTx(ctx, func(tx pgx.Tx) error {
		if err := execSQL(ctx, tx, `DELETE FROM delegation_credential WHERE principal_id = $1`, principal); err != nil {
			return err
		}
		return revokeInitiatorAuthority(ctx, tx, principal)
	})
}

func revokeInitiatorAuthority(ctx context.Context, tx pgx.Tx, principal string) error {
	roots, err := queryMany(ctx, tx, `SELECT session_id::text FROM session_authority WHERE initiator_id = $1 AND parent_session_id IS NULL ORDER BY session_id`, pgx.RowTo[string], principal)
	if err != nil {
		return err
	}
	for _, id := range roots {
		if err := lockSessionAuthority(ctx, tx, id); err != nil {
			return err
		}
		if err := execSQL(ctx, tx, `UPDATE session_authority SET revoked = TRUE WHERE session_id = $1`, id); err != nil {
			return err
		}
		if err := queueAuthorityCancellation(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// CaptureDelegationCredential replaces only the observed version and extends
// that initiator's nonrevoked grants atomically. A concurrent registration or
// revocation wins through the version fence.
func (c *Client) CaptureDelegationCredential(ctx context.Context, principal string, expected int64, ciphertext []byte, expiry time.Time) (bool, error) {
	if expected == 0 {
		tag, err := c.db.Exec(ctx, `INSERT INTO delegation_credential (principal_id, ciphertext) VALUES ($1, $2) ON CONFLICT (principal_id) DO NOTHING`, principal, ciphertext)
		return tag.RowsAffected() == 1, err
	}
	var saved bool
	err := c.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
 UPDATE delegation_credential SET ciphertext = $3, version = version + 1, refresh_lease = NULL, refresh_expires_at = NULL
 WHERE principal_id = $1 AND version = $2
 `, principal, expected, ciphertext)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		saved = true
		return extendInitiatorAuthority(ctx, tx, principal, expiry)
	})
	return saved && err == nil, err
}

// extendInitiatorAuthority serializes credential rotation with child creation
// and revocation. It never reactivates a revoked root or narrows a later grant.
func extendInitiatorAuthority(ctx context.Context, tx pgx.Tx, principal string, expiry time.Time) error {
	roots, err := lockInitiatorRoots(ctx, tx, principal)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return nil
	}
	return execSQL(ctx, tx, `
 UPDATE session_authority SET expires_at = $2
 WHERE initiator_id = $1 AND expires_at IS NOT NULL AND expires_at < $2 AND NOT revoked
   AND root_session_id IN (SELECT session_id FROM session_authority WHERE initiator_id = $1 AND parent_session_id IS NULL AND NOT revoked)
 `, principal, expiry)
}

func lockInitiatorRoots(ctx context.Context, tx pgx.Tx, principal string) ([]string, error) {
	roots, err := queryMany(ctx, tx, `SELECT session_id::text FROM session_authority WHERE initiator_id = $1 AND parent_session_id IS NULL AND NOT revoked ORDER BY session_id`, pgx.RowTo[string], principal)
	if err != nil {
		return nil, err
	}
	for _, id := range roots {
		if err := lockSessionAuthority(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return roots, nil
}
