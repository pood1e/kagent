package session

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
)

type cancellationFixture struct {
	a2asrv.RequestHandler
	mu                  sync.Mutex
	completed, canceled []string
	sessionErr          error
}

func (*cancellationFixture) ClaimTaskCancellations(_ context.Context, limit int) ([]database.TaskCancellation, error) {
	return []database.TaskCancellation{{TaskID: "working", SessionID: "working"}, {TaskID: "unavailable", SessionID: "unavailable"}}, nil
}

func (f *cancellationFixture) GetSessionByID(_ context.Context, id string) (*apiv1.Session, error) {
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	return &apiv1.Session{Id: id, Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}}, nil
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
	if request.Tenant != "team-a/assistant" {
		return nil, errors.New("missing accepted Session tenant")
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

func TestCancellationRecoveryResolvesSessionLifecycle(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		completed []string
	}{
		{name: "deleted Session", err: database.ErrNotFound, completed: []string{"working", "unavailable"}},
		{name: "store unavailable", err: errors.New("database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &cancellationFixture{sessionErr: test.err}
			require.NoError(t, NewCancellations(fixture, fixture).tick(t.Context()))
			require.ElementsMatch(t, test.completed, fixture.completed)
			require.Empty(t, fixture.canceled)
		})
	}
}
