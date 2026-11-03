package session

import (
	"context"
	"slices"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// BindForkAuthority creates fresh initiator authority within the checkpoint's
// fixed bounds and any current runtime parent's ceiling. Discovery never expands
// a retained all-tools selection during restore.
func (s *Service) BindForkAuthority(ctx context.Context, id, creator string, source *database.CheckpointExecution) (*apiv1.SessionAuthority, error) {
	if s.authority == nil {
		return nil, serviceerrors.NewUnavailable("Execution authority is not configured", nil)
	}
	if source == nil || source.Revision == nil || source.Revision.Dependencies.GetDirect() == nil || source.Authority.GetScopeCeiling().GetResources() == nil {
		return nil, serviceerrors.NewFailedPrecondition("Checkpoint requires a fixed resource ceiling", nil)
	}
	ceiling := source.Authority.ScopeCeiling
	if err := auth.ValidateResourceCeiling(ceiling.Resources); err != nil {
		return nil, serviceerrors.NewFailedPrecondition("Invalid checkpoint resource ceiling", err)
	}
	if !auth.ResourceGrantsWithin(source.Revision.Dependencies.Direct, ceiling.Resources) ||
		!auth.ResourceGrantsWithin(source.Revision.Dependencies.Delegated, ceiling.Resources) {
		return nil, serviceerrors.NewFailedPrecondition("Checkpoint dependencies exceed its resource ceiling", nil)
	}
	grant, err := s.authority.Bind(ctx, id, creator, ceiling)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Cannot establish fork authority", err)
	}
	if err := s.bindWorkspace(ctx, source.Revision.Namespace, grant); err != nil {
		return nil, err
	}
	if grant.InitiatorId != source.Authority.InitiatorId || grant.WorkspaceUid == "" || grant.WorkspaceUid != source.Authority.WorkspaceUid {
		return nil, serviceerrors.NewPermissionDenied("Checkpoint execution identity changed", nil)
	}
	if grant.ParentSessionId != "" && !auth.ResourceGrantsWithin(ceiling.Resources, grant.ScopeCeiling.Resources) {
		return nil, serviceerrors.NewPermissionDenied("Checkpoint exceeds parent resource ceiling", nil)
	}
	check, err := s.dependencyChecker(ctx, grant.ParentSessionId)
	if err != nil {
		return nil, err
	}
	workspace := &apiv1.WorkspaceAuthorizationReference{Namespace: source.Revision.Namespace, Uid: grant.WorkspaceUid}
	requirements := proto.CloneOf(ceiling.Resources)
	if err := requireSessionCreation(workspace, requirements); err != nil {
		return nil, err
	}
	if err := requireDependencies(ctx, check, workspace, requirements); err != nil {
		return nil, err
	}
	grant.ScopeCeiling.Resources = requirements
	grant.ScopeCeiling.Scopes = slices.DeleteFunc(grant.ScopeCeiling.Scopes, func(scope string) bool { return !slices.Contains(ceiling.Scopes, scope) })
	return grant, nil
}

// CheckForkAuthority rechecks the stored fork, including its originating parent,
// before provisioning. It also fences concurrent creation-receipt reuse.
func (s *Service) CheckForkAuthority(ctx context.Context, session *apiv1.Session, grant *apiv1.SessionAuthority) error {
	return s.authorizeReservationRetry(ctx, session, &apiv1.Session{Agent: session.Agent, Authority: grant}, grant.ScopeCeiling)
}
