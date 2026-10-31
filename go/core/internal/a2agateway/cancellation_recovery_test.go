package a2agateway

import (
	"context"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"

	"github.com/kagent-dev/kagent/go/core/internal/database"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
)

type cancellationRecoveryStore struct {
	*gatewayTestStore
	completed chan struct{}
}

func (s *cancellationRecoveryStore) ClaimTaskCancellations(context.Context, int) ([]database.TaskCancellation, error) {
	return []database.TaskCancellation{{SessionID: s.session.Id, TaskID: string(s.task.ID)}}, nil
}

func (s *cancellationRecoveryStore) CompleteTaskCancellation(context.Context, database.TaskCancellation) error {
	s.completed <- struct{}{}
	return nil
}

func TestCancellationRecoveryRoutesThroughNativeGateway(t *testing.T) {
	session := gatewayTestSession()
	store := &cancellationRecoveryStore{
		gatewayTestStore: &gatewayTestStore{session: session, task: &a2a.Task{
			ID: "accepted-task", ContextID: session.ContextId,
			Status: a2a.TaskStatus{State: a2a.TaskStateCanceled},
		}},
		completed: make(chan struct{}, 1),
	}
	gateway := newTestGateway(store.gatewayTestStore, &gatewayTestAuthorizer{}, &gatewayTestDialer{}, gatewayTestURL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sessionsvc.NewCancellations(store, gateway).Start(ctx) }()
	select {
	case <-store.completed:
	case <-time.After(2 * time.Second):
		t.Fatal("accepted Session tenant did not reach native cancellation")
	}
	cancel()
	require.NoError(t, <-done)
}
