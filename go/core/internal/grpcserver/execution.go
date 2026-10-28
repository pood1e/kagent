package grpcserver

import (
	"context"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/execution"
)

type executionServer struct {
	apiv1.UnimplementedExecutionServiceServer
	service *execution.Service
}

func (s *executionServer) GetTaskExecution(ctx context.Context, request *apiv1.GetTaskExecutionRequest) (*apiv1.GetTaskExecutionResponse, error) {
	result, err := s.service.Get(ctx, request.TaskId)
	if err != nil {
		return nil, err
	}
	return &apiv1.GetTaskExecutionResponse{Execution: result}, nil
}
func (s *executionServer) ListTaskExecutions(ctx context.Context, request *apiv1.ListTaskExecutionsRequest) (*apiv1.ListTaskExecutionsResponse, error) {
	return s.service.List(ctx, request)
}

func (s *executionServer) GetSessionAuthority(ctx context.Context, request *apiv1.GetSessionAuthorityRequest) (*apiv1.GetSessionAuthorityResponse, error) {
	return s.service.GetAuthority(ctx, request.SessionId)
}
func (s *executionServer) RevokeSessionAuthority(ctx context.Context, request *apiv1.RevokeSessionAuthorityRequest) (*apiv1.RevokeSessionAuthorityResponse, error) {
	if err := s.service.RevokeAuthority(ctx, request.SessionId); err != nil {
		return nil, err
	}
	return &apiv1.RevokeSessionAuthorityResponse{}, nil
}
func (s *executionServer) GetRuntimeAuthority(ctx context.Context, _ *apiv1.GetRuntimeAuthorityRequest) (*apiv1.GetRuntimeAuthorityResponse, error) {
	return s.service.RuntimeAuthority(ctx)
}
func (s *executionServer) RegisterDelegationCredential(ctx context.Context, request *apiv1.RegisterDelegationCredentialRequest) (*apiv1.RegisterDelegationCredentialResponse, error) {
	if err := s.service.RegisterCredential(ctx, request.RefreshToken); err != nil {
		return nil, err
	}
	return &apiv1.RegisterDelegationCredentialResponse{}, nil
}
func (s *executionServer) RevokeDelegationCredential(ctx context.Context, _ *apiv1.RevokeDelegationCredentialRequest) (*apiv1.RevokeDelegationCredentialResponse, error) {
	if err := s.service.RevokeCredential(ctx); err != nil {
		return nil, err
	}
	return &apiv1.RevokeDelegationCredentialResponse{}, nil
}

func (s *executionServer) GetDelegationCredentialStatus(ctx context.Context, _ *apiv1.GetDelegationCredentialStatusRequest) (*apiv1.GetDelegationCredentialStatusResponse, error) {
	return s.service.CredentialStatus(ctx)
}
func (s *executionServer) IssueSessionCredential(ctx context.Context, request *apiv1.IssueSessionCredentialRequest) (*apiv1.IssueSessionCredentialResponse, error) {
	return s.service.IssueSessionCredential(ctx, request)
}
