package database

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func fixedToolGrant(name string) *apiv1.ResourceGrant {
	return &apiv1.ResourceGrant{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{McpTool: &apiv1.MCPToolAuthorizationReference{
		Server: &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER, Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: "tools"}, Uid: "server-1"}, Name: name,
	}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL}}
}
func TestDurableResourceCeilingsNarrowChildrenAndRejectEscalation(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	root := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	root.Authority = &apiv1.SessionAuthority{SessionId: root.Id, InitiatorId: root.Creator, RootSessionId: root.Id, WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{Resources: &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{fixedToolGrant("Read"), fixedToolGrant("delete")}}}}
	stored, _, err := client.CreateSession(t.Context(), root, uuid.NewString())
	require.NoError(t, err)
	grant, active, err := client.GetSessionAuthority(t.Context(), stored.Id)
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, proto.Equal(root.Authority.ScopeCeiling.Resources, grant.ScopeCeiling.Resources))
	child := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	child.Authority = &apiv1.SessionAuthority{SessionId: child.Id, InitiatorId: root.Creator, ParentSessionId: root.Id, RootSessionId: root.Id, WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{Resources: &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{fixedToolGrant("Read")}}}}
	_, _, err = client.CreateSession(t.Context(), child, uuid.NewString())
	require.NoError(t, err)
	narrowed, active, err := client.GetSessionAuthority(t.Context(), child.Id)
	require.NoError(t, err)
	require.True(t, active)
	require.Len(t, narrowed.ScopeCeiling.Resources.Grants, 1)
	denied := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	denied.Authority = proto.CloneOf(child.Authority)
	denied.Authority.SessionId = denied.Id
	denied.Authority.ParentSessionId = child.Id
	denied.Authority.ScopeCeiling.Resources = &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{fixedToolGrant("delete")}}
	_, _, err = client.CreateSession(t.Context(), denied, uuid.NewString())
	require.ErrorIs(t, err, ErrFailedPrecondition)
	_, err = client.GetSessionByID(t.Context(), denied.Id)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = client.db.Exec(t.Context(), `UPDATE session_authority SET resource_grants=$2 WHERE session_id=$1`, root.Id, []byte{0xff})
	require.NoError(t, err)
	_, _, err = client.GetSessionAuthority(t.Context(), child.Id)
	require.Error(t, err, "malformed ancestor grants fail closed")
}

func TestSessionReservationCASMatchesTheAuthorizedRevision(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision-1", "assistant", "kagent")
	revision, err := client.GetAgentRuntimeRevision(t.Context(), "team-a", "assistant")
	require.NoError(t, err)
	request := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	request.PreparedRevision = revision.Revision
	sessionFixture(t, client, t.Context(), "team-a", "revision-2", "assistant", "kagent")
	requestID := uuid.NewString()
	_, _, err = client.CreateSession(t.Context(), request, requestID)
	require.ErrorIs(t, err, ErrConflict)
	request.PreparedRevision = "revision-2"
	result, created, err := client.CreateSession(t.Context(), request, requestID)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "revision-2", result.PreparedRevision)
}

func TestDependencyManifestCodecPreservesDirectAndDelegatedAuthority(t *testing.T) {
	input := &apiv1.ExecutionDependencyManifest{
		Direct:    &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{fixedToolGrant("Read")}},
		Delegated: &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{fixedToolGrant("delete")}},
	}
	data, err := encodeDependencyManifest(input)
	require.NoError(t, err)
	decoded, err := decodeDependencyManifest(data)
	require.NoError(t, err)
	require.True(t, proto.Equal(input, decoded))
	input.Direct.Grants[0].Resource.GetMcpTool().Server.Uid = "recreated"
	require.Equal(t, "server-1", decoded.Direct.Grants[0].Resource.GetMcpTool().Server.Uid)
	_, err = decodeDependencyManifest([]byte{0xff})
	require.Error(t, err)
	_, err = encodeDependencyManifest(&apiv1.ExecutionDependencyManifest{})
	require.Error(t, err)
}
