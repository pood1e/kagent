package session

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type continuationContextKey struct{}

// durableWorkerKey marks contexts rebuilt by ExecutionContext. Only in-process
// workers create them, so they act for the control plane.
type durableWorkerKey struct{}

// controlPlaneCaller reports whether ctx belongs to the in-process control
// plane, either directly or as a durable worker with execution authority.
func controlPlaneCaller(ctx context.Context) bool {
	if worker, _ := ctx.Value(durableWorkerKey{}).(bool); worker {
		return true
	}
	caller, _ := auth.AuthSessionFrom(ctx)
	_, controlPlane := caller.(auth.ControlPlaneSession)
	return controlPlane
}

func dispatchOrigin(ctx context.Context, request *a2a.SendMessageRequest) *database.DispatchOrigin {
	if id, ok := ctx.Value(continuationContextKey{}).(uuid.UUID); ok {
		return &database.DispatchOrigin{ContinuationID: id}
	}
	if origin := runtimeauth.Origin(ctx); origin != nil {
		return &database.DispatchOrigin{SessionID: origin.SessionID, Background: request.Config != nil && request.Config.ReturnImmediately}
	}
	return nil
}
