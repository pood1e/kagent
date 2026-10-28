package session

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/require"

	"github.com/kagent-dev/kagent/go/core/internal/database"
)

type cancellationFixture struct {
	a2asrv.RequestHandler
	mu                  sync.Mutex
	completed, canceled []string
}

func (*cancellationFixture) ClaimTaskCancellations(_ context.Context, limit int) ([]database.TaskCancellation, error) {
	return []database.TaskCancellation{{TaskID: "working"}, {TaskID: "unavailable"}}, nil
}

func (f *cancellationFixture) CompleteTaskCancellation(_ context.Context, claim database.TaskCancellation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, claim.TaskID)
	return nil
}

func (f *cancellationFixture) CancelTask(ctx context.Context, request *a2a.CancelTaskRequest) (*a2a.Task, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("missing cleanup deadline")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, string(request.ID))
	if request.ID == "unavailable" {
		return nil, errors.New("Actor unavailable")
	}
	return &a2a.Task{ID: request.ID, Status: a2a.TaskStatus{State: a2a.TaskStateCanceled}}, nil
}

func TestCancellationRecoveryAcknowledgesOnlyTerminalCleanup(t *testing.T) {
	fixture := &cancellationFixture{}
	require.NoError(t, NewCancellations(fixture, fixture).tick(t.Context()))
	require.ElementsMatch(t, []string{"working", "unavailable"}, fixture.canceled)
	require.Equal(t, []string{"working"}, fixture.completed)
}
