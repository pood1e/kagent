package database

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func testAuthorizationChange() AuthorizationChange {
	return AuthorizationChange{
		RequestID: uuid.NewString(), ActorID: "admin", StoreID: "store", ModelID: "model", Grant: true,
		Binding: &apiv1.RoleBinding{
			Subject: &apiv1.SubjectReference{Subject: &apiv1.SubjectReference_PrincipalId{PrincipalId: "alice"}},
			Role:    apiv1.AccessRole_ACCESS_ROLE_EXECUTOR,
			Scope: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
				Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team", Uid: "workspace"},
			}},
		},
	}
}

func TestAuthorizationChangePersistsIntentAndConfirmation(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	change := testAuthorizationChange()
	for range 2 {
		applied, err := client.BeginAuthorizationChange(t.Context(), change)
		require.NoError(t, err)
		require.False(t, applied)
	}
	require.NoError(t, client.CompleteAuthorizationChange(t.Context(), change.RequestID))
	applied, err := client.BeginAuthorizationChange(t.Context(), change)
	require.NoError(t, err)
	require.True(t, applied)
	for _, mutate := range []func(*AuthorizationChange){
		func(c *AuthorizationChange) { c.ActorID = "other" },
		func(c *AuthorizationChange) { c.StoreID = "other" },
		func(c *AuthorizationChange) { c.ModelID = "other" },
		func(c *AuthorizationChange) { c.Grant = false },
		func(c *AuthorizationChange) { c.Binding.Role = apiv1.AccessRole_ACCESS_ROLE_ACCESS_ADMIN },
	} {
		changed := change
		changed.Binding = proto.CloneOf(change.Binding)
		mutate(&changed)
		_, err := client.BeginAuthorizationChange(t.Context(), changed)
		require.ErrorIs(t, err, ErrIdempotencyConflict)
	}
	require.ErrorIs(t, client.CompleteAuthorizationChange(t.Context(), uuid.NewString()), ErrNotFound)
}

func TestAuthorizationChangeConcurrentRetriesAndCancellation(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	change := testAuthorizationChange()
	start := make(chan struct{})
	failures := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			_, err := client.BeginAuthorizationChange(t.Context(), change)
			failures <- err
		})
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.NoError(t, client.CompleteAuthorizationChange(t.Context(), change.RequestID))
	applied, err := client.BeginAuthorizationChange(t.Context(), change)
	require.NoError(t, err)
	require.True(t, applied)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled := testAuthorizationChange()
	_, err = client.BeginAuthorizationChange(ctx, canceled)
	require.ErrorIs(t, err, context.Canceled)
	canceled.ActorID = "a fresh intent"
	_, err = client.BeginAuthorizationChange(t.Context(), canceled)
	require.NoError(t, err)
}

func TestAuthorityWorkspaceIdentityPersistsAndConstrainsDelegation(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	root := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	root.Authority = &apiv1.SessionAuthority{
		SessionId: root.Id, InitiatorId: root.Creator, RootSessionId: root.Id,
		WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{},
	}
	_, _, err := client.CreateSession(t.Context(), root, uuid.NewString())
	require.NoError(t, err)
	prior, _, err := client.GetSessionAuthority(t.Context(), root.Id)
	require.NoError(t, err)
	require.Equal(t, "workspace-1", prior.WorkspaceUid)
	for _, workspace := range []string{"workspace-1", "workspace-2"} {
		child := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
		child.Authority = proto.CloneOf(root.Authority)
		child.Authority.SessionId = child.Id
		child.Authority.ParentSessionId = root.Id
		child.Authority.WorkspaceUid = workspace
		_, _, err := client.CreateSession(t.Context(), child, uuid.NewString())
		if workspace == "workspace-1" {
			require.NoError(t, err)
			loaded, err := client.GetSessionByID(t.Context(), child.Id)
			require.NoError(t, err)
			require.Equal(t, workspace, loaded.Authority.WorkspaceUid)
		} else {
			require.ErrorIs(t, err, ErrFailedPrecondition)
			_, err := client.GetSessionByID(t.Context(), child.Id)
			require.ErrorIs(t, err, ErrNotFound)
		}
	}
}
