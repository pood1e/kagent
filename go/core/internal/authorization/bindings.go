package authorization

import (
	"context"
	"errors"

	"buf.build/go/protovalidate"
	fga "github.com/openfga/go-sdk"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (s *Service) GrantRole(ctx context.Context, request *apiv1.GrantRoleRequest) (*apiv1.GrantRoleResponse, error) {
	if err := protovalidate.Validate(request); err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid role grant", err)
	}
	binding, err := s.mutate(ctx, request.RequestId, request.Binding, request.Workspace, true)
	if err != nil {
		return nil, err
	}
	return &apiv1.GrantRoleResponse{Binding: binding}, nil
}

func (s *Service) RevokeRole(ctx context.Context, request *apiv1.RevokeRoleRequest) error {
	if err := protovalidate.Validate(request); err != nil {
		return serviceerrors.NewInvalidArgument("Invalid role revocation", err)
	}
	_, err := s.mutate(ctx, request.RequestId, request.Binding, request.Workspace, false)
	return err
}

func (s *Service) mutate(ctx context.Context, requestID string, binding *apiv1.RoleBinding, workspace *apiv1.WorkspaceAuthorizationReference, grant bool) (*apiv1.RoleBinding, error) {
	principal, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	resource, err := s.resolveBindingScope(ctx, binding.Scope, workspace)
	if err != nil {
		return nil, err
	}
	relation, ok := roleRelations[binding.Role][resource.kind]
	if !ok {
		return nil, serviceerrors.NewInvalidArgument("Role does not apply to scope", nil)
	}
	if group := binding.Subject.GetGroup(); group != nil && (s.issuer == "" || group.Issuer != s.issuer) {
		return nil, serviceerrors.NewInvalidArgument("Group issuer differs from the configured identity provider", nil)
	}
	if err := s.authorizeBinding(ctx, principal, resource, binding.Role); err != nil {
		return nil, err
	}
	subject, err := encodeSubject(binding.Subject)
	if err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid role subject", err)
	}
	canonical := proto.CloneOf(binding)
	canonical.Scope = proto.CloneOf(resource.scope)
	change := database.AuthorizationChange{
		RequestID: requestID, ActorID: principal.ID,
		StoreID: s.config.StoreId, ModelID: s.config.AuthorizationModelId,
		Binding: canonical, Grant: grant,
	}
	applied, err := s.store.BeginAuthorizationChange(ctx, change)
	if errors.Is(err, database.ErrIdempotencyConflict) {
		return nil, serviceerrors.NewAlreadyExists("request_id belongs to another authorization change", nil)
	}
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Failed to record authorization change", err)
	}
	if applied {
		return canonical, nil
	}
	actor, err := principalSubject(principal.ID)
	if err != nil {
		return nil, serviceerrors.NewUnauthenticated("Invalid authenticated identity", err)
	}
	if err := s.applyMutation(ctx, requestID, actor, fga.TupleKey{User: subject, Relation: relation, Object: resource.object}, grant); err != nil {
		return nil, err
	}
	if err := s.store.CompleteAuthorizationChange(ctx, requestID); err != nil {
		return nil, serviceerrors.NewUnavailable("Confirm authorization change by retrying the same request_id", err)
	}
	return canonical, nil
}

func (s *Service) authorizeBinding(ctx context.Context, principal auth.Principal, resource *resolvedResource, role apiv1.AccessRole) error {
	allowed, err := s.check(ctx, principal, resource, apiv1.Permission_PERMISSION_ACCESS_MANAGE)
	if err != nil {
		return err
	}
	if !allowed && role == apiv1.AccessRole_ACCESS_ROLE_EXECUTION_READER && (resource.kind == sessionScope || resource.kind == taskScope) {
		allowed, err = s.check(ctx, principal, resource, apiv1.Permission_PERMISSION_SESSION_SHARE)
		if err != nil {
			return err
		}
	}
	if !allowed {
		return serviceerrors.NewPermissionDenied("Role binding access denied", nil)
	}
	return nil
}

func (s *Service) resolveBindingScope(ctx context.Context, scope *apiv1.AuthorizationResource, workspace *apiv1.WorkspaceAuthorizationReference) (*resolvedResource, error) {
	resource, err := s.resolver.resolve(ctx, scope)
	if serviceerrors.IsCode(err, serviceerrors.CodeNotFound) || serviceerrors.IsCode(err, serviceerrors.CodeFailedPrecondition) {
		return nil, serviceerrors.NewPermissionDenied("Role binding access denied", nil)
	}
	if err == nil && !matchesWorkspace(resource, workspace) {
		return nil, serviceerrors.NewPermissionDenied("Role binding access denied", nil)
	}
	return resource, err
}
