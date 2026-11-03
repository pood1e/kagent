package session

import (
	"context"

	"github.com/google/uuid"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// CaptureExecutionAuthority fixes current initiator dependencies for future
// automated execution. Runtime creation remains a separately authorized step.
func (s *Service) CaptureExecutionAuthority(ctx context.Context, agent *apiv1.ResourceReference, creator string) (*apiv1.SessionAuthority, error) {
	if s.authority == nil {
		return nil, serviceerrors.NewUnavailable("Execution authority is not configured", nil)
	}
	grant, err := s.authority.Bind(ctx, uuid.NewString(), creator, nil)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Cannot capture execution authority", err)
	}
	if grant.ParentSessionId != "" || grant.InitiatorId != creator {
		return nil, serviceerrors.NewPermissionDenied("Independent initiator authority required", nil)
	}
	if err := s.bindWorkspace(ctx, agent.Namespace, grant); err != nil {
		return nil, err
	}
	if _, err := s.bindDependencies(ctx, agent, grant, nil); err != nil {
		return nil, err
	}
	return grant, nil
}

// CheckCapturedAuthority rechecks an independent initiator's retained resource
// set. Schedule retries and triggers retain their original UID-bound selection.
func (s *Service) CheckCapturedAuthority(ctx context.Context, agent *apiv1.ResourceReference, grant *apiv1.SessionAuthority) error {
	if grant.GetInitiatorId() == "" || grant.GetWorkspaceUid() == "" || grant.InitiatorId != authPrincipal(ctx).ID || grant.GetScopeCeiling().GetResources() == nil {
		return serviceerrors.NewPermissionDenied("Captured initiator resource ceiling required", nil)
	}
	if s.permissions == nil {
		return serviceerrors.NewUnavailable("Resource execution authorization is not configured", nil)
	}
	if err := auth.ValidateResourceCeiling(grant.ScopeCeiling.Resources); err != nil {
		return serviceerrors.NewFailedPrecondition("Invalid captured resource ceiling", err)
	}
	workspace := &apiv1.WorkspaceAuthorizationReference{Namespace: agent.Namespace, Uid: grant.WorkspaceUid}
	if err := requireSessionCreation(workspace, grant.ScopeCeiling.Resources); err != nil {
		return err
	}
	return requireDependencies(ctx, s.permissions.CheckPermissions, workspace, grant.ScopeCeiling.Resources)
}

// ProvisioningContext authorizes a retained automated execution before creating
// an Actor. Current direct requirements and the complete captured set both apply.
func (s *Service) ProvisioningContext(ctx context.Context, id string) (context.Context, error) {
	ctx, err := s.ExecutionContext(ctx, id)
	if err != nil {
		return nil, err
	}
	principal := authPrincipal(ctx)
	if err := s.authorizer.Check(ctx, principal, auth.VerbCreate, auth.Resource{Type: "Session", Name: id}); err != nil {
		return nil, serviceerrors.NewPermissionDenied("Execution scope denied", err)
	}
	session, err := s.store.GetSessionByID(ctx, id)
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Cannot resolve provisioning Session", err)
	}
	check := func(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
		return s.permissions.CheckExecutionPermissions(ctx, principal, request)
	}
	grant := principal.Execution
	workspace := &apiv1.WorkspaceAuthorizationReference{Namespace: session.Agent.Namespace, Uid: grant.WorkspaceUid}
	if err := requireSessionCreation(workspace, grant.ScopeCeiling.Resources); err != nil {
		return nil, err
	}
	if err := requireDependencies(ctx, check, workspace, grant.ScopeCeiling.Resources); err != nil {
		return nil, err
	}
	return ctx, nil
}

func requireSessionCreation(workspace *apiv1.WorkspaceAuthorizationReference, resources *apiv1.ResourceGrantSet) error {
	resource := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{Workspace: workspace}}
	if !auth.ResourceGrantAllows(resources, resource, apiv1.Permission_PERMISSION_SESSION_CREATE) {
		return serviceerrors.NewPermissionDenied("Captured Session creation authority required", nil)
	}
	return nil
}
