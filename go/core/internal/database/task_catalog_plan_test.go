package database

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestCatalogReadPlansForBoundScopeAndRestoresPoolSession(t *testing.T) {
	config := setupTestDB(t).Config()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	client := NewClient(pool, "public")
	readMode := func() string {
		t.Helper()
		var mode string
		require.NoError(t, pool.QueryRow(t.Context(), `SHOW plan_cache_mode`).Scan(&mode))
		return mode
	}
	before := readMode()
	for _, failure := range []error{nil, errors.New("abort catalog read")} {
		err := catalogRead(t.Context(), client, func(tx pgx.Tx) error {
			var mode, readOnly, isolation string
			require.NoError(t, tx.QueryRow(t.Context(), `SELECT current_setting('plan_cache_mode'),current_setting('transaction_read_only'),current_setting('transaction_isolation')`).Scan(&mode, &readOnly, &isolation))
			require.Equal(t, "force_custom_plan", mode)
			require.Equal(t, "on", readOnly)
			require.Equal(t, "repeatable read", isolation)
			return failure
		})
		require.ErrorIs(t, err, failure)
		require.Equal(t, before, readMode(), "commit and rollback restore the same pooled connection")
	}
}
