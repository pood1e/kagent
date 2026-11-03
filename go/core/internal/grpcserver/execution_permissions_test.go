package grpcserver

import (
	"context"
	"slices"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// Scope/credential transport tests use a deterministic resource checker. The
// native authorization suite owns real OpenFGA resource decisions.
type authorityTestPermissions struct{}

func (authorityTestPermissions) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	response := &apiv1.CheckPermissionsResponse{}
	for _, grant := range request.Checks {
		response.Resources = append(response.Resources, &apiv1.ResourceAccess{Resource: proto.CloneOf(grant.Resource), AllowedActions: slices.Clone(grant.Permissions)})
	}
	return response, nil
}
func (p authorityTestPermissions) CheckExecutionPermissions(ctx context.Context, principal auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	response, err := p.CheckPermissions(ctx, request)
	for _, access := range response.Resources {
		access.AllowedActions = slices.DeleteFunc(access.AllowedActions, func(permission apiv1.Permission) bool {
			return !auth.ResourceGrantAllows(principal.Execution.GetScopeCeiling().GetResources(), access.Resource, permission)
		})
	}
	return response, err
}
func authorityTestDependencies(server string, tools []string, model string) *apiv1.ExecutionDependencyManifest {
	result := &apiv1.ResourceGrantSet{}
	for _, name := range tools {
		result.Grants = append(result.Grants, &apiv1.ResourceGrant{
			Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{McpTool: &apiv1.MCPToolAuthorizationReference{
				Server: &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER, Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: server}, Uid: "server-uid"}, Name: name,
			}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL},
		})
	}
	if model != "" {
		result.Grants = append(result.Grants, &apiv1.ResourceGrant{
			Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION, Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: model}, Uid: "model-uid"}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MODEL_CONNECTION_USE},
		})
	}
	return &apiv1.ExecutionDependencyManifest{Direct: result}
}
