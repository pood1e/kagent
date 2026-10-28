package migrations

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestSessionAuthorityRequiresExplicitHistoricalBinding(t *testing.T) {
	dsn, config := startTestDB(t)
	sources := BuiltinSourcesInSchema(false, "public", "public")
	if err := WithProvider(t.Context(), config, sources[0], func(provider *goose.Provider) error {
		_, err := provider.UpTo(t.Context(), 3)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Historical storage ownership does not establish a verified execution root.
	execSQL(t, dsn, `
		INSERT INTO a2a_context (id, context_id)
		VALUES ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001');
		INSERT INTO runtime_instance (id, kind, user_id, request_id, state)
		VALUES ('00000000-0000-0000-0000-000000000001', 'agent', 'https://issuer.example#legacy-sub', 'historical', 'RUNTIME_STATE_SUSPENDED');
		INSERT INTO session (id, data, context_id, history_id)
		VALUES ('00000000-0000-0000-0000-000000000001', '', '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001');
	`)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		if err := RunUp(t.Context(), config, sources); err != nil {
			t.Fatal(err)
		}
		var authorities int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM session_authority").Scan(&authorities); err != nil {
			t.Fatal(err)
		}
		if authorities != 0 {
			t.Fatalf("historical execution authorities = %d, want 0 before explicit binding", authorities)
		}
		var owner string
		if err := db.QueryRowContext(t.Context(), "SELECT user_id FROM session_record").Scan(&owner); err != nil {
			t.Fatal(err)
		}
		if owner != "https://issuer.example#legacy-sub" {
			t.Fatalf("historical owner = %q, want original storage owner", owner)
		}
		if err := WithProvider(t.Context(), config, sources[0], func(provider *goose.Provider) error {
			_, err := provider.DownTo(t.Context(), 3)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if testTableExists(t, dsn, "session_authority") {
			t.Fatal("session_authority remains after DownTo(3)")
		}
	}
}
