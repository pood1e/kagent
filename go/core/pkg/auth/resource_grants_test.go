package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func toolGrant(name, uid string) *apiv1.ResourceGrant {
	return &apiv1.ResourceGrant{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{McpTool: &apiv1.MCPToolAuthorizationReference{
		Server: &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "tools"}, Uid: uid}, Name: name,
	}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL}}
}
func TestResourceGrantSetsAreCanonicalOwnedAndExact(t *testing.T) {
	input := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{toolGrant("read", "server-1"), toolGrant("Read", "server-1"), toolGrant("read", "server-1")}}
	canonical, err := CanonicalResourceGrants(input)
	require.NoError(t, err)
	require.Len(t, canonical.Grants, 2)
	reversed := proto.CloneOf(input)
	reversed.Grants[0], reversed.Grants[1] = reversed.Grants[1], reversed.Grants[0]
	other, err := CanonicalResourceGrants(reversed)
	require.NoError(t, err)
	require.True(t, proto.Equal(canonical, other))
	ceiling := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{toolGrant("Read", "server-1")}}
	narrowed := IntersectResourceGrants(canonical, ceiling)
	require.True(t, proto.Equal(ceiling, narrowed))
	require.False(t, ResourceGrantsWithin(canonical, ceiling))
	require.True(t, ResourceGrantsWithin(narrowed, ceiling))
	input.Grants[1].Resource.GetMcpTool().Name = "changed"
	require.True(t, ResourceGrantAllows(canonical, toolGrant("Read", "server-1").Resource, apiv1.Permission_PERMISSION_MCP_TOOL_CALL))
	require.False(t, ResourceGrantAllows(canonical, toolGrant("Read", "server-2").Resource, apiv1.Permission_PERMISSION_MCP_TOOL_CALL))
	narrowed.Grants[0].Resource.GetMcpTool().Name = "mutated"
	require.Equal(t, "Read", ceiling.Grants[0].Resource.GetMcpTool().Name)
	require.Empty(t, IntersectResourceGrants(canonical, nil).Grants)
	err = ValidateResourceCeiling(&apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{toolGrant("read", "")}})
	require.Error(t, err)
}
