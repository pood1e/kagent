package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/kagent-dev/kagent/go/pkg/logging"
)

type cancellationStore interface {
	ClaimTaskCancellations(context.Context, int) ([]database.TaskCancellation, error)
	CompleteTaskCancellation(context.Context, database.TaskCancellation) error
	GetSessionByID(context.Context, string) (*apiv1.Session, error)
}

// Cancellations recovers recursive cancellation independently of client streams
// and controller replicas. The durable tree remains fenced after completion.
type Cancellations struct {
	store   cancellationStore
	gateway a2asrv.RequestHandler
}

var _ manager.Runnable = (*Cancellations)(nil)

func NewCancellations(store cancellationStore, gateway a2asrv.RequestHandler) *Cancellations {
	return &Cancellations{store: store, gateway: gateway}
}
func (*Cancellations) NeedLeaderElection() bool { return false }
func (c *Cancellations) Start(ctx context.Context) error {
	ctx = auth.AuthSessionTo(ctx, auth.ControlPlaneSession{})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := c.tick(ctx); err != nil && ctx.Err() == nil {
			logging.FromContext(ctx).WarnContext(ctx, "native cancellation recovery unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (c *Cancellations) tick(ctx context.Context) error {
	claims, err := c.store.ClaimTaskCancellations(ctx, 4)
	if err != nil {
		return err
	}
	var group sync.WaitGroup
	for _, claim := range claims {
		group.Go(func() {
			attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			err := c.cancel(attempt, claim)
			if err != nil && ctx.Err() == nil {
				logging.FromContext(ctx).WarnContext(ctx, "native descendant cancellation pending", "task_id", claim.TaskID, "error", err)
			}
		})
	}
	group.Wait()
	return nil
}

func (c *Cancellations) cancel(ctx context.Context, claim database.TaskCancellation) error {
	session, err := c.store.GetSessionByID(ctx, claim.SessionID)
	if errors.Is(err, database.ErrNotFound) {
		return c.store.CompleteTaskCancellation(ctx, claim)
	}
	if err != nil {
		return err
	}
	ctx = runtimeauth.CancellationContext(ctx, claim.SessionID, claim.TaskID)
	result, err := c.gateway.CancelTask(ctx, &a2a.CancelTaskRequest{
		Tenant: session.GetAgent().GetNamespace() + "/" + session.GetAgent().GetName(),
		ID:     a2a.TaskID(claim.TaskID),
	})
	if err == nil && result != nil && result.Status.State.Terminal() {
		return c.store.CompleteTaskCancellation(ctx, claim)
	}
	return err
}
