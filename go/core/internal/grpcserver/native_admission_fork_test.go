package grpcserver

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/kagent-dev/kagent/go/core/internal/database"
)

// admitRuntimeTask creates a turn through a reserved dispatch, as native task
// admission requires.
func createRuntimeTask(t *testing.T, store *database.Client, sessionID string, mutationHash []byte, task *a2a.Task) (int64, error) {
	t.Helper()
	dispatch := uuid.New()
	if err := store.ReserveSessionDispatch(t.Context(), sessionID, dispatch, "", nil); err != nil {
		return 0, err
	}
	return store.CreateRuntimeTask(t.Context(), sessionID, mutationHash, task, dispatch.String())
}
