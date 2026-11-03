package session

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type dispatchTestAuthority struct {
	creationAuthority
	session *apiv1.Session
}

func (a dispatchTestAuthority) Resolve(_ context.Context, id string) (auth.Session, error) {
	if a.session.Id != id {
		return nil, database.ErrNotFound
	}
	return auth.NewCredentialSession(auth.Principal{ID: a.session.Creator, Execution: a.session.Authority}, nil), nil
}

func dispatchTestService(t *testing.T, session *apiv1.Session) *Service {
	t.Helper()
	session.Creator = "alice"
	direct := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{sessionTestTool("Read")}}
	session.Authority = &apiv1.SessionAuthority{SessionId: session.Id, RootSessionId: session.Id, InitiatorId: session.Creator, WorkspaceUid: "workspace-1",
		ScopeCeiling: &apiv1.ScopeCeiling{Resources: proto.CloneOf(direct)}}
	session.PreparedRevision = uuid.NewString()
	storage := &serviceTestStore{getResult: session, revision: &database.RuntimeRevision{
		Namespace: session.GetAgent().GetRef().GetNamespace(), Dependencies: &apiv1.ExecutionDependencyManifest{Direct: direct},
	}}
	return NewService(storage, serviceTestAuthorizer{}, nil,
		WithExecutionAuthority(dispatchTestAuthority{session: session}), WithResourceAuthorization(creationPermissions{}))
}

func sessionTestTool(name string) *apiv1.ResourceGrant {
	return &apiv1.ResourceGrant{Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{McpTool: &apiv1.MCPToolAuthorizationReference{
		Server: &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER, Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: "tools"}, Uid: "server-1"}, Name: name,
	}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL}}
}

type deniedDispatchPermissions struct {
	creationPermissions
	sessionID string
}

func (p deniedDispatchPermissions) CheckExecutionPermissions(ctx context.Context, principal auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	if p.sessionID != "" && principal.Execution.SessionId != p.sessionID {
		return p.creationPermissions.CheckExecutionPermissions(ctx, principal, request)
	}
	response := &apiv1.CheckPermissionsResponse{}
	for _, grant := range request.Checks {
		response.Resources = append(response.Resources, &apiv1.ResourceAccess{Resource: proto.CloneOf(grant.Resource)})
	}
	return response, nil
}

func TestDispatchAndContinuationRecheckBeforeWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Service)
		want   error
	}{
		{name: "allowed"},
		{name: "revoked tool", change: func(s *Service) { s.permissions = deniedDispatchPermissions{} }, want: a2a.ErrUnauthorized},
		{name: "engine unavailable", change: func(s *Service) {
			s.permissions = creationPermissions{err: serviceerrors.NewUnavailable("OpenFGA unavailable", nil)}
		}, want: a2a.ErrInternalError},
		{name: "absent authority", change: func(s *Service) { s.authority = nil }, want: a2a.ErrInternalError},
		{name: "absent manifest", change: func(s *Service) { s.store.(*serviceTestStore).revision.Dependencies = nil }, want: a2a.ErrUnsupportedOperation},
		{name: "unbounded execution", change: func(s *Service) { s.store.(*serviceTestStore).getResult.Authority.ScopeCeiling.Resources = nil }, want: a2a.ErrUnauthorized},
		{name: "outside ceiling", change: func(s *Service) {
			s.store.(*serviceTestStore).revision.Dependencies.Direct.Grants[0] = sessionTestTool("delete")
		}, want: a2a.ErrUnauthorized},
		{name: "cross workspace revision", change: func(s *Service) { s.store.(*serviceTestStore).revision.Namespace = "other-team" }, want: a2a.ErrUnsupportedOperation},
	} {
		t.Run(test.name, func(t *testing.T) {
			stored := &apiv1.Session{Id: uuid.NewString(), Agent: &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}}, State: apiv1.RuntimeState_RUNTIME_STATE_READY, Operation: apiv1.RuntimeOperation_RUNTIME_OPERATION_NONE}
			stored.ContextId = stored.Id
			sessions := dispatchTestService(t, stored)
			if test.change != nil {
				test.change(sessions)
			}
			writes := &interactionTestStore{}
			service := NewInteractionService(writes, nil, sessions, false)
			request := &a2a.SendMessageRequest{Message: &a2a.Message{ID: "message", ContextID: stored.Id}, Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{URL: "https://notify.example/callback"}}}
			prepared, err := service.PrepareSend(serviceTestContext("alice"), types.NamespacedName{Namespace: "team-a", Name: "assistant"}, request)
			if test.want == nil {
				require.NoError(t, err)
				require.NotEqual(t, uuid.Nil, prepared.DispatchID)
				require.Equal(t, 1, writes.pushWrites)
				require.Equal(t, 1, writes.dispatchWrites)
				_, err = sessions.ExecutionContext(t.Context(), stored.Id)
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.want)
			require.Nil(t, prepared)
			require.Zero(t, writes.pushWrites)
			require.Zero(t, writes.dispatchWrites)
			_, err = sessions.ExecutionContext(t.Context(), stored.Id)
			require.Error(t, err, "durable continuation must apply the same dependency gate")
		})
	}
}

func TestActorCannotDispatchAWiderSameRootSession(t *testing.T) {
	parent := &apiv1.Session{Id: uuid.NewString(), Creator: "owner", Agent: &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}}}
	child := &apiv1.Session{Id: uuid.NewString(), Creator: "owner", Agent: parent.Agent}
	service, _ := continuationTestSessions(t, parent, child, serviceTestAuthorizer{})
	parent.Authority.ScopeCeiling.Resources = proto.CloneOf(parent.Authority.ScopeCeiling.Resources)
	parent.Authority.ScopeCeiling.Resources.Grants = append(parent.Authority.ScopeCeiling.Resources.Grants, sessionTestTool("delete"))
	ctx := auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(auth.Principal{ID: "actor", RuntimeBinding: &auth.RuntimeBinding{SessionID: child.Id}}, nil))
	_, err := service.sendAuthority(ctx, parent.Id, false)
	require.Equal(t, serviceerrors.CodePermissionDenied, serviceerrors.CodeOf(err), "a common execution root cannot widen a child's delegated capabilities")
	child.Authority.ScopeCeiling.Resources = proto.CloneOf(parent.Authority.ScopeCeiling.Resources)
	_, err = service.sendAuthority(ctx, parent.Id, false)
	require.NoError(t, err)
	service.permissions = deniedDispatchPermissions{sessionID: child.Id}
	_, err = service.sendAuthority(ctx, parent.Id, false)
	require.Equal(t, serviceerrors.CodePermissionDenied, serviceerrors.CodeOf(err), "dispatch requires the originating Session's current dependencies")
	_, err = service.dependencyChecker(ctx, child.Id)
	require.Equal(t, serviceerrors.CodePermissionDenied, serviceerrors.CodeOf(err), "child creation requires the originating Session's current dependencies")
}
