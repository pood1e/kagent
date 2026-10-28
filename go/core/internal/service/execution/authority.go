package execution

import (
	"context"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (s *Service) GetAuthority(ctx context.Context, id string) (*apiv1.GetSessionAuthorityResponse, error) {
	if _, err := s.sessions.Get(ctx, id); err != nil {
		return nil, err
	}
	if s.authority == nil {
		return nil, serviceerrors.NewFailedPrecondition("Execution authority is not configured", nil)
	}
	result, err := s.authority.ReadAuthority(ctx, id)
	if err != nil {
		return nil, serviceerrors.NewInternal("Cannot read execution authority", err)
	}
	return result, nil
}
func (s *Service) RevokeAuthority(ctx context.Context, id string) error {
	return s.sessions.RevokeExecutionAuthority(ctx, id)
}

func (s *Service) RuntimeAuthority(ctx context.Context) (*apiv1.GetRuntimeAuthorityResponse, error) {
	caller, ok := auth.AuthSessionFrom(ctx)
	if !ok || caller.Principal().RuntimeBinding == nil {
		return nil, serviceerrors.NewPermissionDenied("Verified runtime identity is required", nil)
	}
	id := caller.Principal().RuntimeBinding.SessionID
	if _, err := s.sessions.Get(ctx, id); err != nil {
		return nil, err
	}
	if s.authority == nil {
		return nil, serviceerrors.NewFailedPrecondition("Execution authority is not configured", nil)
	}
	resolved, err := s.authority.Resolve(ctx, id)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Execution authority is unavailable", err)
	}
	return &apiv1.GetRuntimeAuthorityResponse{Authority: resolved.Principal().Execution, EffectiveScopes: resolved.Principal().Scopes}, nil
}
func (s *Service) RegisterCredential(ctx context.Context, refreshToken string) error {
	if s.authority == nil {
		return serviceerrors.NewFailedPrecondition("Execution authority is not configured", nil)
	}
	if err := s.authority.RegisterCredential(ctx, refreshToken); err != nil {
		return serviceerrors.NewPermissionDenied("Cannot register delegation credential", err)
	}
	return nil
}
func (s *Service) RevokeCredential(ctx context.Context) error {
	if s.authority == nil {
		return serviceerrors.NewFailedPrecondition("Execution authority is not configured", nil)
	}
	if err := s.authority.RevokeCredential(ctx); err != nil {
		return serviceerrors.NewPermissionDenied("Cannot revoke delegation credential", err)
	}
	return nil
}

func (s *Service) CredentialStatus(ctx context.Context) (*apiv1.GetDelegationCredentialStatusResponse, error) {
	if s.authority == nil {
		return nil, serviceerrors.NewFailedPrecondition("Execution authority is not configured", nil)
	}
	renewable, err := s.authority.CredentialStatus(ctx)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Cannot read delegation credential", err)
	}
	return &apiv1.GetDelegationCredentialStatusResponse{Renewable: renewable}, nil
}
func (s *Service) IssueSessionCredential(ctx context.Context, request *apiv1.IssueSessionCredentialRequest) (*apiv1.IssueSessionCredentialResponse, error) {
	if _, err := s.sessions.Get(ctx, request.SessionId); err != nil {
		return nil, err
	}
	if s.authority == nil {
		return nil, serviceerrors.NewFailedPrecondition("Execution authority is not configured", nil)
	}
	result, err := s.authority.IssueSessionCredential(ctx, request)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Cannot issue Session credential", err)
	}
	return result, nil
}
