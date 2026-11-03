package scheduledrun

import (
	"context"
	"errors"

	"github.com/google/uuid"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

// ExecutionAuthorization captures and rechecks immutable automation capabilities.
type ExecutionAuthorization interface {
	CaptureExecutionAuthority(context.Context, *apiv1.ResourceReference, string) (*apiv1.SessionAuthority, error)
	CheckCapturedAuthority(context.Context, *apiv1.ResourceReference, *apiv1.SessionAuthority) error
}

func (s *Service) checkAuthority(ctx context.Context, schedule *apiv1.ScheduledRun) error {
	if s.execution == nil {
		return serviceerrors.NewUnavailable("Scheduled execution authorization is not configured", nil)
	}
	id, err := uuid.Parse(schedule.Id)
	if err != nil {
		return serviceerrors.NewInternal("Invalid persisted schedule identity", err)
	}
	grant, err := s.store.GetScheduledRunAuthority(ctx, id, schedule.Creator)
	if errors.Is(err, database.ErrFailedPrecondition) {
		return serviceerrors.NewFailedPrecondition("Schedule requires captured execution authority", err)
	}
	if err != nil {
		return storeError(err)
	}
	return s.execution.CheckCapturedAuthority(ctx, schedule.Agent, grant)
}
