package session

import (
	"context"
	"errors"
	"slices"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (s *Service) reserveAuthorizedSession(ctx context.Context, request *apiv1.Session, requestID string, ceiling *apiv1.ScopeCeiling) (*apiv1.Session, error) {
	existing, err := s.store.GetSessionByRequestID(ctx, request.Creator, requestID)
	if err == nil {
		return existing, s.authorizeReservationRetry(ctx, existing, request, ceiling)
	}
	if !errors.Is(err, database.ErrNotFound) {
		return nil, err
	}
	revision, err := s.bindDependencies(ctx, request.Agent, request.Authority, ceiling)
	if err != nil {
		return nil, err
	}
	request.PreparedRevision = revision
	result, created, err := s.store.CreateSession(ctx, request, requestID)
	if err != nil {
		return nil, err
	}
	if !created {
		if err := s.authorizeReservationRetry(ctx, result, request, ceiling); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Service) authorizeReservationRetry(ctx context.Context, existing, request *apiv1.Session, requested *apiv1.ScopeCeiling) error {
	original, current := existing.GetAuthority(), request.GetAuthority()
	if !proto.Equal(existing.Agent, request.Agent) || original.GetInitiatorId() != current.GetInitiatorId() ||
		original.GetParentSessionId() != current.GetParentSessionId() || original.GetWorkspaceUid() != current.GetWorkspaceUid() ||
		current.GetParentSessionId() != "" && original.GetRootSessionId() != current.GetRootSessionId() {
		return database.ErrIdempotencyConflict
	}
	if existing.State == apiv1.RuntimeState_RUNTIME_STATE_DELETED {
		return database.ErrFailedPrecondition
	}
	resources := original.GetScopeCeiling().GetResources()
	if resources == nil || current.GetScopeCeiling() == nil {
		return serviceerrors.NewFailedPrecondition("Session requires a fixed resource ceiling", nil)
	}
	if s.permissions == nil {
		return serviceerrors.NewUnavailable("Resource execution authorization is not configured", nil)
	}
	for _, scope := range original.ScopeCeiling.Scopes {
		if !slices.Contains(current.ScopeCeiling.Scopes, scope) {
			return serviceerrors.NewPermissionDenied("Existing Session exceeds requested scope ceiling", nil)
		}
	}
	if requested.GetResources() != nil && !auth.ResourceGrantsWithin(resources, requested.Resources) ||
		current.ParentSessionId != "" && !auth.ResourceGrantsWithin(resources, current.ScopeCeiling.Resources) {
		return serviceerrors.NewPermissionDenied("Existing Session exceeds execution resource ceiling", nil)
	}
	check := s.permissions.CheckPermissions
	if current.ParentSessionId != "" {
		parent, err := s.authority.Resolve(ctx, current.ParentSessionId)
		if err != nil {
			return serviceerrors.NewPermissionDenied("Parent authority unavailable", err)
		}
		check = func(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
			return s.permissions.CheckExecutionPermissions(ctx, parent.Principal(), request)
		}
	}
	return requireDependencies(ctx, check, &apiv1.WorkspaceAuthorizationReference{Namespace: existing.Agent.Namespace, Uid: original.WorkspaceUid}, resources)
}
