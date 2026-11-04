package execution

import (
	"context"
	"slices"

	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type mcpSelection struct {
	Tools []string `json:"tools"`
}

func (s *Service) runtimeSelection(ctx context.Context, principal auth.Principal, revision *database.RuntimeRevision, server, model string) (any, error) {
	if s.permissions == nil {
		return nil, serviceerrors.NewUnavailable("Resource execution authorization is not configured", nil)
	}
	ceiling := principal.Execution.GetScopeCeiling().GetResources()
	if revision.Dependencies.GetDirect() == nil || ceiling == nil {
		return nil, serviceerrors.NewPermissionDenied("Bound execution resource ceiling required", nil)
	}
	workspace := &apiv1.WorkspaceAuthorizationReference{Namespace: revision.Namespace, Uid: principal.Execution.WorkspaceUid}
	var base, targets []*apiv1.ResourceGrant
	for _, grant := range revision.Dependencies.Direct.Grants {
		ref := grant.Resource.GetKubernetes()
		if grant.Resource.GetMcpTool() != nil || ref.GetKind() == apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER {
			if server != "" {
				targets = append(targets, mcpTargetGrants(grant, ceiling, server)...)
			}
		} else if ref.GetKind() == apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION {
			if server == "" && (model == "" || model == ref.GetRef().GetName()) {
				targets = append(targets, grant)
			}
		} else {
			base = append(base, grant)
		}
	}
	baseAccess, err := s.checkRuntimeGrants(ctx, principal, workspace, base)
	if err != nil {
		return nil, err
	}
	if len(baseAccess) != len(base) {
		return nil, serviceerrors.NewPermissionDenied("Runtime dependency denied", nil)
	}
	allowed, err := s.checkRuntimeGrants(ctx, principal, workspace, targets)
	if err != nil {
		return nil, err
	}
	if (server != "" || model != "") && len(allowed) == 0 {
		return nil, serviceerrors.NewPermissionDenied("Runtime target denied", nil)
	}
	if server != "" {
		selection := mcpSelection{Tools: []string{}}
		for _, grant := range allowed {
			selection.Tools = append(selection.Tools, grant.Resource.GetMcpTool().Name)
		}
		slices.Sort(selection.Tools)
		selection.Tools = slices.Compact(selection.Tools)
		return selection, nil
	}
	models := []corev1.LocalObjectReference{}
	for _, grant := range allowed {
		models = append(models, corev1.LocalObjectReference{Name: grant.Resource.GetKubernetes().Ref.Name})
	}
	return models, nil
}

func mcpTargetGrants(grant *apiv1.ResourceGrant, ceiling *apiv1.ResourceGrantSet, target string) []*apiv1.ResourceGrant {
	if tool := grant.Resource.GetMcpTool(); tool != nil {
		if tool.Server.Ref.Namespace+"/"+tool.Server.Ref.Name == target {
			return []*apiv1.ResourceGrant{grant}
		}
		return nil
	}
	server := grant.Resource.GetKubernetes()
	if server.Ref.Namespace+"/"+server.Ref.Name != target || !slices.Contains(grant.Permissions, apiv1.Permission_PERMISSION_MCP_SERVER_USE) {
		return nil
	}
	var result []*apiv1.ResourceGrant
	for _, candidate := range ceiling.Grants {
		if tool := candidate.Resource.GetMcpTool(); tool != nil && proto.Equal(tool.Server, server) && slices.Contains(candidate.Permissions, apiv1.Permission_PERMISSION_MCP_TOOL_CALL) {
			result = append(result, &apiv1.ResourceGrant{Resource: candidate.Resource, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL}})
		}
	}
	return result
}

func (s *Service) checkRuntimeGrants(ctx context.Context, principal auth.Principal, workspace *apiv1.WorkspaceAuthorizationReference, grants []*apiv1.ResourceGrant) ([]*apiv1.ResourceGrant, error) {
	var result []*apiv1.ResourceGrant
	for batch := range slices.Chunk(grants, auth.MaxPermissionChecks) {
		response, err := s.permissions.CheckExecutionPermissions(ctx, principal, &apiv1.CheckPermissionsRequest{Workspace: workspace, Checks: batch})
		if err != nil {
			return nil, err
		}
		if len(response.GetResources()) != len(batch) {
			return nil, serviceerrors.NewUnavailable("Incomplete runtime authorization", nil)
		}
		for i, access := range response.Resources {
			if proto.Equal(access.Resource, batch[i].Resource) && slices.Equal(access.AllowedActions, batch[i].Permissions) {
				result = append(result, batch[i])
			}
		}
	}
	return result, nil
}
