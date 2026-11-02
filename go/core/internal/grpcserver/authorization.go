package grpcserver

import (
	"context"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/authorization"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

type authorizationServer struct {
	apiv1.UnimplementedAuthorizationServiceServer
	service *authorization.Service
}

var _ apiv1.AuthorizationServiceServer = (*authorizationServer)(nil)

func (s *authorizationServer) configured() (*authorization.Service, error) {
	if s.service == nil {
		return nil, serviceerrors.NewUnavailable("Resource authorization is not configured", nil)
	}
	return s.service, nil
}

func (s *authorizationServer) ListAccessRoles(ctx context.Context, _ *apiv1.ListAccessRolesRequest) (*apiv1.ListAccessRolesResponse, error) {
	service, err := s.configured()
	if err != nil {
		return nil, err
	}
	return service.ListAccessRoles(ctx)
}

func (s *authorizationServer) ListWorkspaces(ctx context.Context, request *apiv1.ListWorkspacesRequest) (*apiv1.ListWorkspacesResponse, error) {
	service, err := s.configured()
	if err != nil {
		return nil, err
	}
	return service.ListWorkspaces(ctx, request)
}

func (s *authorizationServer) CheckPermissions(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	service, err := s.configured()
	if err != nil {
		return nil, err
	}
	return service.CheckPermissions(ctx, request)
}

func (s *authorizationServer) ListRoleBindings(ctx context.Context, request *apiv1.ListRoleBindingsRequest) (*apiv1.ListRoleBindingsResponse, error) {
	service, err := s.configured()
	if err != nil {
		return nil, err
	}
	return service.ListRoleBindings(ctx, request)
}

func (s *authorizationServer) GrantRole(ctx context.Context, request *apiv1.GrantRoleRequest) (*apiv1.GrantRoleResponse, error) {
	service, err := s.configured()
	if err != nil {
		return nil, err
	}
	return service.GrantRole(ctx, request)
}

func (s *authorizationServer) RevokeRole(ctx context.Context, request *apiv1.RevokeRoleRequest) (*apiv1.RevokeRoleResponse, error) {
	service, err := s.configured()
	if err != nil {
		return nil, err
	}
	if err := service.RevokeRole(ctx, request); err != nil {
		return nil, err
	}
	return &apiv1.RevokeRoleResponse{}, nil
}
