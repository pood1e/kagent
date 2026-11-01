package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaskCatalogUsesExistingSharedTrigramExtension(t *testing.T) {
	dsn, config := startTestDB(t)
	execSQL(t, dsn, `CREATE SCHEMA shared_extensions; CREATE EXTENSION pg_trgm WITH SCHEMA shared_extensions`)
	for _, schema := range []string{"tenant_one", "tenant_two"} {
		sources := BuiltinSourcesInSchema(false, schema, "public")
		require.NoError(t, RunUp(t.Context(), config, sources))
		require.NoError(t, VerifyMigrated(t.Context(), config, sources))
		for _, index := range []string{"session_catalog_name_idx", "session_task_catalog_prompt_idx", "session_task_catalog_active_idx"} {
			require.True(t, testTableExists(t, dsn, schema+"."+index))
		}
	}
}
