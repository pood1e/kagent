package session

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/executionauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func TestForkCapturesHistoricalBoundsAndCurrentPermissions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*database.CheckpointExecution, *Service)
		want   serviceerrors.Code
	}{
		{name: "fresh lease keeps original tools"},
		{name: "missing ceiling", mutate: func(source *database.CheckpointExecution, _ *Service) { source.Authority.ScopeCeiling.Resources = nil }, want: serviceerrors.CodeFailedPrecondition},
		{name: "incomplete ceiling", mutate: func(source *database.CheckpointExecution, _ *Service) {
			source.Authority.ScopeCeiling.Resources.Grants = source.Authority.ScopeCeiling.Resources.Grants[1:]
		}, want: serviceerrors.CodeFailedPrecondition},
		{name: "missing Session creation grant", mutate: func(source *database.CheckpointExecution, _ *Service) {
			source.Authority.ScopeCeiling.Resources.Grants = source.Authority.ScopeCeiling.Resources.Grants[:2]
		}, want: serviceerrors.CodePermissionDenied},
		{name: "different initiator", mutate: func(source *database.CheckpointExecution, _ *Service) { source.Authority.InitiatorId = "other" }, want: serviceerrors.CodePermissionDenied},
		{name: "recreated workspace", mutate: func(source *database.CheckpointExecution, _ *Service) {
			source.Authority.WorkspaceUid = "old-workspace"
		}, want: serviceerrors.CodePermissionDenied},
		{name: "revoked dependency", mutate: func(_ *database.CheckpointExecution, s *Service) {
			s.permissions = creationPermissions{err: serviceerrors.NewPermissionDenied("dependency revoked", nil)}
		}, want: serviceerrors.CodePermissionDenied},
		{name: "engine unavailable", mutate: func(_ *database.CheckpointExecution, s *Service) {
			s.permissions = creationPermissions{err: serviceerrors.NewUnavailable("OpenFGA unavailable", nil)}
		}, want: serviceerrors.CodeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverUse := &apiv1.ResourceGrant{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{Kubernetes: proto.CloneOf(sessionTestTool("Read").Resource.GetMcpTool().Server)}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_SERVER_USE}}
			resources := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{serverUse, sessionTestTool("Read"), {
				Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team-a", Uid: "workspace-1"}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_SESSION_CREATE},
			}}}
			source := &database.CheckpointExecution{Revision: &database.RuntimeRevision{Namespace: "team-a", Dependencies: &apiv1.ExecutionDependencyManifest{Direct: &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{serverUse}}}},
				Authority: &apiv1.SessionAuthority{SessionId: uuid.NewString(), InitiatorId: "alice", WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{Resources: resources}, ExpiresAt: timestamppb.New(time.Now().Add(-time.Hour))}}
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, v1alpha3.AddToScheme(scheme))
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", UID: "workspace-1"}},
				&v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "tools", UID: "server-1"}, Status: v1alpha3.RemoteMCPServerStatus{DiscoveredTools: []*v1alpha3.MCPTool{{Name: "Read"}, {Name: "delete"}}}},
			).Build()
			authority, err := executionauth.NewService(nil, nil, nil, true)
			require.NoError(t, err)
			service := NewService(nil, serviceTestAuthorizer{}, nil, WithExecutionAuthority(authority), WithWorkspaceIdentity(kube), WithResourceAuthorization(creationPermissions{}))
			if test.mutate != nil {
				test.mutate(source, service)
			}
			original := proto.CloneOf(source.Authority)
			grant, err := service.BindForkAuthority(serviceTestContext("alice"), uuid.NewString(), "alice", source)
			if test.want != "" {
				require.Equal(t, test.want, serviceerrors.CodeOf(err), "%v", err)
				require.Nil(t, grant)
				return
			}
			require.NoError(t, err)
			require.Nil(t, grant.ExpiresAt, "a new credential lease is independent of the expired source")
			require.Empty(t, grant.ParentSessionId)
			require.Equal(t, grant.SessionId, grant.RootSessionId)
			require.True(t, proto.Equal(original, source.Authority), "retained authority remains immutable")
			require.True(t, proto.Equal(source.Authority.ScopeCeiling.Resources, grant.ScopeCeiling.Resources))
			require.Len(t, grant.ScopeCeiling.Resources.Grants, 3, "new discovery and wider current access cannot add tools")
		})
	}
}
