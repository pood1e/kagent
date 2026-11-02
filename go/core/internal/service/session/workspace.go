package session

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

// WithWorkspaceIdentity captures namespace identity before reserving a Session.
func WithWorkspaceIdentity(reader client.Reader) Option {
	return func(s *Service) { s.workspaces = reader }
}

func (s *Service) bindWorkspace(ctx context.Context, namespace string, grant *apiv1.SessionAuthority) error {
	if s.workspaces == nil {
		return nil
	}
	var workspace corev1.Namespace
	if err := s.workspaces.Get(ctx, types.NamespacedName{Name: namespace}, &workspace); err != nil {
		return serviceerrors.NewUnavailable("Failed to resolve workspace identity", err)
	}
	if workspace.UID == "" || grant == nil {
		return serviceerrors.NewFailedPrecondition("Workspace and execution identities are required", nil)
	}
	uid := string(workspace.UID)
	if grant.ParentSessionId != "" && grant.WorkspaceUid != uid {
		return serviceerrors.NewPermissionDenied("Delegation must retain its workspace identity", nil)
	}
	grant.WorkspaceUid = uid
	return nil
}
