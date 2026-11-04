package session

import (
	"context"
	"errors"
	"slices"

	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// ResourcePermissions checks current credentials or a trusted resolved initiator.
// The latter also applies its immutable ancestor ceiling.
type ResourcePermissions interface {
	CheckPermissions(context.Context, *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error)
	CheckExecutionPermissions(context.Context, auth.Principal, *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error)
}

// WithResourceAuthorization captures exact dependency actions before reservation.
func WithResourceAuthorization(permissions ResourcePermissions) Option {
	return func(s *Service) { s.permissions = permissions }
}

func (s *Service) bindDependencies(ctx context.Context, agent *apiv1.ResourceReference, grant *apiv1.SessionAuthority, requested *apiv1.ScopeCeiling) (string, error) {
	if s.permissions == nil || grant.GetScopeCeiling() == nil || grant.WorkspaceUid == "" {
		return "", serviceerrors.NewUnavailable("Resource execution authorization is not configured", nil)
	}
	revision, err := s.store.GetAgentRuntimeRevision(ctx, agent.Namespace, agent.Name)
	if errors.Is(err, database.ErrNotFound) {
		return "", serviceerrors.NewFailedPrecondition("Agent does not have a ready prepared revision", err)
	}
	if err != nil {
		return "", serviceerrors.NewInternal("Cannot resolve execution dependencies", err)
	}
	manifest := revision.Dependencies
	if manifest == nil || manifest.Direct == nil {
		return "", serviceerrors.NewFailedPrecondition("Prepared revision requires a dependency manifest", nil)
	}
	candidates := &apiv1.ResourceGrantSet{Grants: append(slices.Clone(manifest.Direct.Grants), manifest.GetDelegated().GetGrants()...)}
	dependencies, err := auth.CanonicalResourceGrants(candidates)
	if err != nil || dependencies == nil {
		return "", serviceerrors.NewFailedPrecondition("Prepared revision requires UID-bound dependencies", err)
	}
	if err := auth.ValidateResourceCeiling(dependencies); err != nil {
		return "", serviceerrors.NewFailedPrecondition("Prepared dependency identities are unbounded", err)
	}
	dependencies.Grants = append(dependencies.Grants, &apiv1.ResourceGrant{
		Resource:    &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: agent.Namespace, Uid: grant.WorkspaceUid}}},
		Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_SESSION_CREATE},
	})
	check, err := s.dependencyChecker(ctx, grant.ParentSessionId)
	if err != nil {
		return "", err
	}
	workspace := &apiv1.WorkspaceAuthorizationReference{Namespace: agent.Namespace, Uid: grant.WorkspaceUid}
	if err := requireDependencies(ctx, check, workspace, dependencies); err != nil {
		return "", err
	}
	if err := s.expandMCPTools(ctx, dependencies); err != nil {
		return "", err
	}
	dependencies, err = auth.CanonicalResourceGrants(dependencies)
	if err != nil {
		return "", serviceerrors.NewFailedPrecondition("Invalid resolved dependency ceiling", err)
	}
	if err := requireDependencies(ctx, check, workspace, dependencies); err != nil {
		return "", err
	}
	if grant.ParentSessionId != "" && !auth.ResourceGrantsWithin(dependencies, grant.ScopeCeiling.Resources) ||
		requested.GetResources() != nil && !auth.ResourceGrantsWithin(dependencies, requested.Resources) {
		return "", serviceerrors.NewPermissionDenied("Dependencies exceed the execution resource ceiling", nil)
	}
	grant.ScopeCeiling.Resources = dependencies
	return revision.Revision, nil
}

func (s *Service) expandMCPTools(ctx context.Context, dependencies *apiv1.ResourceGrantSet) error {
	for _, grant := range slices.Clone(dependencies.Grants) {
		server := grant.Resource.GetKubernetes()
		if server.GetKind() != apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER || !slices.Contains(grant.Permissions, apiv1.Permission_PERMISSION_MCP_SERVER_USE) {
			continue
		}
		if s.workspaces == nil {
			return serviceerrors.NewUnavailable("MCP dependency discovery unavailable", nil)
		}
		current := new(v1alpha3.RemoteMCPServer)
		if err := s.workspaces.Get(ctx, types.NamespacedName{Namespace: server.Ref.Namespace, Name: server.Ref.Name}, current); err != nil {
			return serviceerrors.NewFailedPrecondition("Cannot resolve MCP dependency", err)
		}
		if string(current.UID) != server.Uid || current.Generation != current.Status.ObservedGeneration || len(current.Status.DiscoveredTools) == 0 {
			return serviceerrors.NewFailedPrecondition("MCP dependency discovery is not current", nil)
		}
		for _, tool := range current.Status.DiscoveredTools {
			if tool == nil {
				return serviceerrors.NewFailedPrecondition("Invalid discovered MCP tool", nil)
			}
			dependencies.Grants = append(dependencies.Grants, &apiv1.ResourceGrant{
				Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{
					McpTool: &apiv1.MCPToolAuthorizationReference{Server: proto.CloneOf(server), Name: tool.Name},
				}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL},
			})
		}
	}
	return nil
}

func requireDependencies(ctx context.Context, check func(context.Context, *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error), workspace *apiv1.WorkspaceAuthorizationReference, dependencies *apiv1.ResourceGrantSet) error {
	for batch := range slices.Chunk(dependencies.Grants, auth.MaxPermissionChecks) {
		response, err := check(ctx, &apiv1.CheckPermissionsRequest{Workspace: workspace, Checks: batch})
		if err != nil {
			return err
		}
		if len(response.GetResources()) != len(batch) {
			return serviceerrors.NewUnavailable("Incomplete dependency authorization", nil)
		}
		for i, access := range response.Resources {
			if !proto.Equal(access.Resource, batch[i].Resource) {
				return serviceerrors.NewPermissionDenied("Execution dependency identity changed", nil)
			}
			for _, permission := range batch[i].Permissions {
				if !slices.Contains(access.AllowedActions, permission) {
					return serviceerrors.NewPermissionDenied("Execution dependency denied", nil)
				}
			}
		}
	}
	return nil
}

func (s *Service) dependencyChecker(ctx context.Context, parentID string) (func(context.Context, *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error), error) {
	if s.permissions == nil {
		return nil, serviceerrors.NewUnavailable("Resource execution authorization is not configured", nil)
	}
	if parentID == "" {
		return s.permissions.CheckPermissions, nil
	}
	if s.authority == nil {
		return nil, serviceerrors.NewUnavailable("Execution authority is not configured", nil)
	}
	parent, err := s.authority.Resolve(ctx, parentID)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Parent execution authority unavailable", err)
	}
	if err := s.requireExecution(ctx, parent.Principal()); err != nil {
		return nil, err
	}
	return func(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
		return s.permissions.CheckExecutionPermissions(ctx, parent.Principal(), request)
	}, nil
}
