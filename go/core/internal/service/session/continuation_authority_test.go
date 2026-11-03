package session

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/executionauth"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type continuationAuthorityStore struct {
	*serviceTestStore
	executionauth.Store
	sessions map[string]*apiv1.Session
	revoked  string
}

func (s *continuationAuthorityStore) GetSessionAuthority(ctx context.Context, id string) (*apiv1.SessionAuthority, bool, error) {
	session, err := s.GetSessionByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return session.Authority, id != s.revoked, nil
}

func (s *continuationAuthorityStore) GetSessionByID(_ context.Context, id string) (*apiv1.Session, error) {
	if session := s.sessions[id]; session != nil {
		return session, nil
	}
	return nil, database.ErrNotFound
}

func (s *continuationAuthorityStore) GetSession(ctx context.Context, id, creator string) (*apiv1.Session, error) {
	session, err := s.GetSessionByID(ctx, id)
	if err != nil || session.Creator != creator {
		return nil, database.ErrNotFound
	}
	return session, nil
}

func TestContinuationReauthorizesEachBoundSession(t *testing.T) {
	parentID, childID := uuid.NewString(), uuid.NewString()
	parent := &apiv1.Session{Id: parentID, Creator: "owner", Agent: &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}}}
	child := &apiv1.Session{Id: childID, Creator: "owner", Agent: parent.Agent}
	sessions, store := continuationTestSessions(t, parent, child, nil)
	gateway := &continuationGateway{}
	service := NewContinuations(&continuationResultStore{result: &a2a.Task{ID: "child-task", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}}, gateway, sessions)
	childTaskID := "child-task"
	value := database.TaskContinuation{ID: uuid.New(), ParentSessionID: parentID, ParentTaskID: "parent-task", ChildSessionID: childID, ChildTaskID: &childTaskID}
	ctx := auth.AuthSessionTo(t.Context(), auth.ControlPlaneSession{})
	id, err := service.resume(ctx, value)
	require.NoError(t, err)
	require.Equal(t, "resumed-task", id)
	require.Len(t, gateway.messages, 1)
	require.Equal(t, parentID, gateway.principals[0].Execution.SessionId)
	for _, revoked := range []string{parentID, childID} {
		store.revoked = revoked
		_, err := service.resume(ctx, value)
		require.Error(t, err, "revoked Session %s must prevent continuation", revoked)
		require.Len(t, gateway.messages, 1)
	}
}

func continuationTestSessions(t *testing.T, parent, child *apiv1.Session, policy auth.Authorizer) (*Service, *continuationAuthorityStore) {
	t.Helper()
	resources := &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{{
		Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{Kubernetes: &apiv1.KubernetesAuthorizationReference{
			Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT, Ref: parent.GetAgent().GetRef(), Uid: "agent-uid",
		}}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_INVOKE},
	}}}
	store := &continuationAuthorityStore{serviceTestStore: &serviceTestStore{revision: &database.RuntimeRevision{
		Namespace: parent.GetAgent().GetRef().GetNamespace(), Dependencies: &apiv1.ExecutionDependencyManifest{Direct: resources},
	}}, sessions: map[string]*apiv1.Session{parent.Id: parent, child.Id: child}}
	for _, session := range store.sessions {
		session.Authority = &apiv1.SessionAuthority{SessionId: session.Id, InitiatorId: "owner", RootSessionId: parent.Id, WorkspaceUid: "workspace-1",
			ScopeCeiling: &apiv1.ScopeCeiling{Scopes: []string{"read"}, Resources: resources}}
	}
	child.Authority.ParentSessionId = parent.Id
	authority, err := executionauth.NewService(store, nil, nil, true)
	require.NoError(t, err)
	if policy == nil {
		policy = runtimeauth.NewAuthorizer(auth.NoopAuthorizer{}, nil, authority)
	}
	return NewService(store, policy, nil, WithExecutionAuthority(authority), WithResourceAuthorization(creationPermissions{})), store
}
