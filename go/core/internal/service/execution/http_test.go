package execution

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type runtimePermissions struct {
	denied string
	err    error
}

func (p *runtimePermissions) CheckExecutionPermissions(_ context.Context, principal auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	result := &apiv1.CheckPermissionsResponse{}
	for _, check := range request.Checks {
		access := &apiv1.ResourceAccess{Resource: proto.CloneOf(check.Resource)}
		for _, permission := range check.Permissions {
			if (p.denied == "" || check.Resource.GetMcpTool().GetName() != p.denied) && auth.ResourceGrantAllows(principal.Execution.ScopeCeiling.Resources, check.Resource, permission) {
				access.AllowedActions = append(access.AllowedActions, permission)
			}
		}
		result.Resources = append(result.Resources, access)
	}
	return result, nil
}
func runtimeTool(name, uid string) *apiv1.ResourceGrant {
	return &apiv1.ResourceGrant{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{McpTool: &apiv1.MCPToolAuthorizationReference{
		Server: &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "tools"}, Uid: uid}, Name: name,
	}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL}}
}
func TestRuntimeToolsIntersectCurrentPermissionCeilingAndPinnedUID(t *testing.T) {
	pinned := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{runtimeTool("Read", "server-1"), runtimeTool("delete", "server-1")}}
	ceiling := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{runtimeTool("Read", "server-1")}}
	principal := auth.Principal{ID: "alice", Execution: &apiv1.SessionAuthority{InitiatorId: "alice", WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{Resources: ceiling}}}
	permissions := &runtimePermissions{}
	service := NewService(nil, nil, nil, permissions)
	revision := &database.RuntimeRevision{Namespace: "team", Dependencies: &apiv1.ExecutionDependencyManifest{Direct: pinned}}
	selected, err := service.runtimeSelection(t.Context(), principal, revision, "team/tools", "")
	require.NoError(t, err)
	require.Equal(t, mcpSelection{Tools: []string{"Read"}}, selected)
	for _, target := range []string{"other/tools", "team/missing"} {
		_, err = service.runtimeSelection(t.Context(), principal, revision, target, "")
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	}
	permissions.denied = "Read"
	_, err = service.runtimeSelection(t.Context(), principal, revision, "team/tools", "")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	permissions.denied = ""
	ceiling.Grants[0].Resource.GetMcpTool().Server.Uid = "recreated"
	_, err = service.runtimeSelection(t.Context(), principal, revision, "team/tools", "")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	data, err := json.Marshal(mcpSelection{Tools: []string{}})
	require.NoError(t, err)
	require.JSONEq(t, `{"tools":[]}`, string(data), "an empty selection must never become an all-tools wildcard")
	permissions.err = serviceerrors.NewUnavailable("FGA unavailable", nil)
	_, err = service.runtimeSelection(t.Context(), principal, revision, "team/tools", "")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
}

func TestDelegatedCapabilitiesNeverExpandTheRootActorToolSelection(t *testing.T) {
	direct := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{runtimeTool("Read", "server-1")}}
	delegated := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{runtimeTool("delete", "server-1")}}
	ceiling := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{runtimeTool("Read", "server-1"), runtimeTool("delete", "server-1")}}
	principal := auth.Principal{ID: "alice", Execution: &apiv1.SessionAuthority{InitiatorId: "alice", WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{Resources: ceiling}}}
	service := NewService(nil, nil, nil, &runtimePermissions{})
	selected, err := service.runtimeSelection(t.Context(), principal, &database.RuntimeRevision{Namespace: "team", Dependencies: &apiv1.ExecutionDependencyManifest{Direct: direct, Delegated: delegated}}, "team/tools", "")
	require.NoError(t, err)
	require.Equal(t, mcpSelection{Tools: []string{"Read"}}, selected)
}
