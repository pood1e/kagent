package tool

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/resourceaccess"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (s *Service) checkPermissions(ctx context.Context, namespace string, checks []*apiv1.ResourceGrant) ([]*apiv1.ResourceAccess, error) {
	return resourceaccess.Check(ctx, s.authorizer, namespace, checks)
}

func (s *Service) requirePermission(ctx context.Context, namespace string, resource *apiv1.AuthorizationResource, permission apiv1.Permission) (*apiv1.AuthorizationResource, error) {
	return resourceaccess.Require(ctx, s.authorizer, namespace, resource, permission)
}

func (s *Service) authorizeMCP(ctx context.Context, ref MCPServerRef, toolName string, permission apiv1.Permission) (MCPServerRef, error) {
	if ref.Ref.Namespace == "" || ref.Ref.Name == "" || ref.GroupKind != remoteMCPServerGVK.GroupKind().String() {
		return MCPServerRef{}, serviceerrors.NewInvalidArgument("A namespaced RemoteMCPServer reference is required", nil)
	}
	resource := serverResource(ref)
	server := resource.GetKubernetes()
	if permission == apiv1.Permission_PERMISSION_MCP_TOOL_CALL {
		if strings.TrimSpace(toolName) == "" {
			return MCPServerRef{}, serviceerrors.NewInvalidArgument("MCP tool name is required", nil)
		}
		resource = &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{
			McpTool: &apiv1.MCPToolAuthorizationReference{Server: server, Name: toolName},
		}}
	}
	bound, err := s.requirePermission(ctx, ref.Ref.Namespace, resource, permission)
	if err != nil {
		return MCPServerRef{}, err
	}
	server = bound.GetKubernetes()
	if permission == apiv1.Permission_PERMISSION_MCP_TOOL_CALL {
		if bound.GetMcpTool().GetName() != toolName {
			return MCPServerRef{}, serviceerrors.NewUnavailable("Invalid authorized MCP tool identity", nil)
		}
		server = bound.GetMcpTool().GetServer()
	}
	if server.GetKind() != apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER || server.GetUid() == "" || server.GetRef().GetNamespace() != ref.Ref.Namespace || server.GetRef().GetName() != ref.Ref.Name {
		return MCPServerRef{}, serviceerrors.NewUnavailable("Invalid authorized MCP server identity", nil)
	}
	ref.UID = types.UID(server.Uid)
	return ref, nil
}

func serverResource(ref MCPServerRef) *apiv1.AuthorizationResource {
	return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
		Kubernetes: &apiv1.KubernetesAuthorizationReference{
			Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER,
			Ref:  &apiv1.ResourceReference{Namespace: ref.Ref.Namespace, Name: ref.Ref.Name}, Uid: string(ref.UID),
		},
	}}
}

func workspaceResource(namespace string) *apiv1.AuthorizationResource {
	return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: namespace},
	}}
}

func requireSession(ctx context.Context) error {
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session == nil || session.Principal().ID == "" {
		return serviceerrors.NewUnauthenticated("Authenticated principal required", nil)
	}
	return nil
}
