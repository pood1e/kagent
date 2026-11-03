package session

import (
	"context"
	"errors"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// requireExecution checks pinned direct capabilities before native dispatch or
// continuation. Engine failures stop admission before a dispatch is reserved.
func (s *Service) requireExecution(ctx context.Context, principal auth.Principal) error {
	grant := principal.Execution
	if principal.ID == "" || grant.GetScopeCeiling().GetResources() == nil || grant.WorkspaceUid == "" || grant.InitiatorId != principal.ID {
		return serviceerrors.NewPermissionDenied("Bound execution resource ceiling required", nil)
	}
	if s.permissions == nil {
		return serviceerrors.NewUnavailable("Resource execution authorization is not configured", nil)
	}
	session, err := s.store.GetSessionByID(ctx, grant.SessionId)
	if errors.Is(err, database.ErrNotFound) {
		return serviceerrors.NewNotFound("Execution Session not found", err)
	}
	if err != nil {
		return serviceerrors.NewInternal("Cannot resolve execution Session", err)
	}
	revision, err := s.store.GetRuntimeRevision(ctx, session.PreparedRevision)
	if err != nil {
		return serviceerrors.NewUnavailable("Cannot resolve execution revision", err)
	}
	if revision.Dependencies.GetDirect() == nil || revision.Namespace != session.GetAgent().GetRef().GetNamespace() {
		return serviceerrors.NewFailedPrecondition("Prepared execution dependency manifest required", nil)
	}
	check := func(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
		return s.permissions.CheckExecutionPermissions(ctx, principal, request)
	}
	return requireDependencies(ctx, check, &apiv1.WorkspaceAuthorizationReference{Namespace: revision.Namespace, Uid: grant.WorkspaceUid}, revision.Dependencies.Direct)
}
