package session

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/kagent-dev/kagent/go/core/internal/database"
)

func createRuntimeTask(t *testing.T, store interface {
	ReserveSessionDispatch(context.Context, string, uuid.UUID, string, *database.DispatchOrigin) error
	CreateRuntimeTask(context.Context, string, []byte, *a2a.Task, string) (int64, error)
}, sessionID string, hash []byte, task *a2a.Task, _ string) (int64, error) {
	t.Helper()
	dispatch := uuid.New()
	if err := store.ReserveSessionDispatch(t.Context(), sessionID, dispatch, "", nil); err != nil {
		return 0, err
	}
	return store.CreateRuntimeTask(t.Context(), sessionID, hash, task, dispatch.String())
}
