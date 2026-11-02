package grpcserver

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func verifyNativeMCPPermissions(t *testing.T, authorization apiv1.AuthorizationServiceClient, tools apiv1.ToolServiceClient, kube client.Client,
	admin, user context.Context, subject *apiv1.SubjectReference, workspace *apiv1.WorkspaceAuthorizationReference,
) {
	t.Helper()
	ref := &apiv1.MCPServerReference{Ref: &apiv1.ResourceReference{Namespace: "team", Name: "tools"}, GroupKind: "RemoteMCPServer.api.kagent.dev"}
	server := &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER, Ref: ref.Ref}
	serverScope := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{Kubernetes: server}}
	toolScope := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{
		McpTool: &apiv1.MCPToolAuthorizationReference{Server: server, Name: "move_task"},
	}}
	grant := func(role apiv1.AccessRole, scope *apiv1.AuthorizationResource) *apiv1.RoleBinding {
		result, err := authorization.GrantRole(admin, &apiv1.GrantRoleRequest{Workspace: workspace, RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{Subject: subject, Role: role, Scope: scope}})
		require.NoError(t, err)
		return result.Binding
	}
	revoke := func(binding *apiv1.RoleBinding) {
		_, err := authorization.RevokeRole(admin, &apiv1.RevokeRoleRequest{Workspace: workspace, RequestId: uuid.NewString(), Binding: binding})
		require.NoError(t, err)
	}
	call := func(name string) error {
		_, err := tools.CallMCPAppTool(user, &apiv1.CallMCPAppToolRequest{Server: ref, ToolName: name})
		return err
	}
	listed, err := tools.ListToolServers(user, &apiv1.ListToolServersRequest{})
	require.NoError(t, err)
	require.Empty(t, listed.ToolServers)
	assertGRPCCode(t, call("move_task"), codes.PermissionDenied)
	toolGrant := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, toolScope)
	require.NoError(t, call("move_task"))
	assertGRPCCode(t, call("delete"), codes.PermissionDenied)
	other := proto.CloneOf(ref)
	other.Ref.Namespace = "other-team"
	_, err = tools.CallMCPAppTool(user, &apiv1.CallMCPAppToolRequest{Server: other, ToolName: "move_task"})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = tools.DeleteToolServer(user, &apiv1.DeleteToolServerRequest{Ref: ref.Ref})
	assertGRPCCode(t, err, codes.PermissionDenied)
	reader := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_READER, serverScope)
	listed, err = tools.ListToolServers(user, &apiv1.ListToolServersRequest{})
	require.NoError(t, err)
	require.Len(t, listed.ToolServers, 1)
	require.Len(t, listed.ToolServers[0].DiscoveredTools, 2)
	revoke(toolGrant)
	assertGRPCCode(t, call("move_task"), codes.PermissionDenied)
	serverGrant := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, serverScope)
	require.NoError(t, call("delete"))
	revoke(serverGrant)
	assertGRPCCode(t, call("delete"), codes.PermissionDenied)
	toolGrant = grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, toolScope)
	current := new(v1alpha3.RemoteMCPServer)
	key := client.ObjectKey{Namespace: "team", Name: "tools"}
	require.NoError(t, kube.Get(t.Context(), key, current))
	require.NoError(t, kube.Delete(t.Context(), current))
	current.ResourceVersion, current.UID = "", "replacement-mcp"
	require.NoError(t, kube.Create(t.Context(), current))
	assertGRPCCode(t, call("move_task"), codes.PermissionDenied)
	listed, err = tools.ListToolServers(user, &apiv1.ListToolServersRequest{})
	require.NoError(t, err)
	require.Empty(t, listed.ToolServers)
	// Current resource facts fence the old bindings even while those tuples remain.
	require.Equal(t, "mcp-1", reader.Scope.GetKubernetes().Uid)
	require.Equal(t, "mcp-1", toolGrant.Scope.GetMcpTool().Server.Uid)
}
