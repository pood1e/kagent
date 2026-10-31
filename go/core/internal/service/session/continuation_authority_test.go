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
	store := &continuationAuthorityStore{serviceTestStore: &serviceTestStore{}, sessions: make(map[string]*apiv1.Session)}
	for _, id := range []string{parentID, childID} {
		store.sessions[id] = &apiv1.Session{Id: id, Creator: "owner", Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"},
			Authority: &apiv1.SessionAuthority{SessionId: id, InitiatorId: "owner", RootSessionId: parentID, ScopeCeiling: &apiv1.ScopeCeiling{Scopes: []string{"read"}}}}
	}
	store.sessions[childID].Authority.ParentSessionId = parentID
	authority, err := executionauth.NewService(store, nil, nil, true)
	require.NoError(t, err)
	policy := runtimeauth.NewAuthorizer(auth.NoopAuthorizer{}, nil, authority)
	sessions := NewService(store, policy, nil, WithExecutionAuthority(authority))
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
