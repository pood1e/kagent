package database

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func authoritySession(t *testing.T, client *Client, parent *apiv1.Session, scopes []string) *apiv1.Session {
	t.Helper()
	request := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	grant := &apiv1.SessionAuthority{SessionId: request.Id, InitiatorId: request.Creator, RootSessionId: request.Id, ScopeCeiling: &apiv1.ScopeCeiling{Scopes: scopes}, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}
	if parent != nil {
		prior, _, err := client.GetSessionAuthority(t.Context(), parent.Id)
		require.NoError(t, err)
		grant.ParentSessionId, grant.RootSessionId, grant.ExpiresAt = parent.Id, prior.RootSessionId, prior.ExpiresAt
	}
	request.Authority = grant
	session, _, err := client.CreateSession(t.Context(), request, uuid.NewString())
	require.NoError(t, err)
	session, err = markSessionReady(t.Context(), client, session.Id, "actor.example")
	require.NoError(t, err)
	return session
}

func TestAuthorityCeilingAndRevocationAreDurable(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	root := authoritySession(t, client, nil, []string{"read", "write"})
	child := authoritySession(t, client, root, []string{"read"})
	leaf := authoritySession(t, client, child, []string{})
	other := authoritySession(t, client, nil, []string{"write"})
	grant, active, err := client.GetSessionAuthority(t.Context(), leaf.Id)
	require.NoError(t, err)
	require.True(t, active)
	require.NotNil(t, grant.ScopeCeiling)
	require.Empty(t, grant.ScopeCeiling.Scopes)
	require.Equal(t, root.Id, grant.RootSessionId)
	require.Equal(t, child.Id, grant.ParentSessionId)
	request := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	request.Authority = &apiv1.SessionAuthority{SessionId: request.Id, InitiatorId: root.Creator, ParentSessionId: child.Id, RootSessionId: root.Id, ScopeCeiling: &apiv1.ScopeCeiling{Scopes: []string{"write"}}, ExpiresAt: grant.ExpiresAt}
	_, _, err = client.CreateSession(t.Context(), request, "escalation")
	require.ErrorIs(t, err, ErrFailedPrecondition)
	_, err = client.GetSessionByID(t.Context(), request.Id)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, client.ReserveSessionDispatch(t.Context(), other.Id, uuid.New(), "cross-root", &DispatchOrigin{SessionID: child.Id}), ErrFailedPrecondition)
	require.NoError(t, client.RevokeSessionAuthority(t.Context(), root.Id))
	require.NoError(t, client.RevokeSessionAuthority(t.Context(), root.Id))
	for _, session := range []*apiv1.Session{root, child, leaf} {
		_, active, err := client.GetSessionAuthority(t.Context(), session.Id)
		require.NoError(t, err)
		require.False(t, active)
		require.ErrorIs(t, client.ReserveSessionDispatch(t.Context(), session.Id, uuid.New(), "after-revoke", nil), ErrFailedPrecondition)
	}
	_, active, err = client.GetSessionAuthority(t.Context(), other.Id)
	require.NoError(t, err)
	require.True(t, active)
}

func TestAuthorityRevocationSerializesWithFirstSave(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	for range 10 {
		root := authoritySession(t, client, nil, []string{"read"})
		child := authoritySession(t, client, root, []string{"read"})
		dispatch := uuid.New()
		require.NoError(t, client.ReserveSessionDispatch(t.Context(), child.Id, dispatch, "input", nil))
		task := newSessionTask(uuid.NewString(), "input")
		task.ContextID = child.Id
		start := make(chan struct{})
		var created, canceled error
		var group sync.WaitGroup
		group.Go(func() {
			<-start
			_, created = client.CreateRuntimeTask(t.Context(), child.Id, taskMutationHash("input"), task, dispatch.String())
		})
		group.Go(func() { <-start; canceled = client.RevokeSessionAuthority(t.Context(), root.Id) })
		close(start)
		group.Wait()
		require.NoError(t, canceled)
		if created != nil {
			require.ErrorIs(t, created, ErrFailedPrecondition)
			continue
		}
		claims, err := client.ClaimTaskCancellations(t.Context(), 100)
		require.NoError(t, err)
		var found bool
		for _, claim := range claims {
			if claim.TaskID == string(task.ID) {
				found = true
			}
			require.NoError(t, client.CompleteTaskCancellation(t.Context(), claim))
		}
		require.True(t, found, "a save that wins the race must enter durable cancellation")
		task.Status.State = a2a.TaskStateCanceled
		_, version, err := client.GetVersionedSessionTask(t.Context(), child.Id, string(task.ID))
		require.NoError(t, err)
		_, err = client.UpdateSessionTask(t.Context(), child.Id, version, taskMutationHash("terminal cleanup"), task, task, "")
		require.NoError(t, err)
	}
}

func TestAuthorityDepthBoundAndCredentialRevocation(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	root := authoritySession(t, client, nil, []string{"read"})
	parent := root
	for range 31 {
		parent = authoritySession(t, client, parent, []string{"read"})
	}
	grant, _, err := client.GetSessionAuthority(t.Context(), parent.Id)
	require.NoError(t, err)
	request := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	request.Authority = &apiv1.SessionAuthority{SessionId: request.Id, InitiatorId: root.Creator, ParentSessionId: parent.Id, RootSessionId: root.Id, ScopeCeiling: &apiv1.ScopeCeiling{Scopes: []string{"read"}}, ExpiresAt: grant.ExpiresAt}
	_, _, err = client.CreateSession(t.Context(), request, "too deep")
	require.ErrorIs(t, err, ErrFailedPrecondition)
	require.NoError(t, client.SaveDelegationCredential(t.Context(), root.Creator, make([]byte, 64)))
	require.NoError(t, client.DeleteDelegationCredential(t.Context(), root.Creator))
	_, active, err := client.GetSessionAuthority(t.Context(), parent.Id)
	require.NoError(t, err)
	require.False(t, active)
	_, err = client.GetDelegationCredential(t.Context(), root.Creator)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestNativeAuthorityRequiresMaterializedCeiling(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	request := newSessionRequest(uuid.NewString(), "assistant", "kagent", "")
	request.Authority = &apiv1.SessionAuthority{SessionId: request.Id, InitiatorId: request.Creator, RootSessionId: request.Id}
	_, _, err := client.CreateSession(t.Context(), request, uuid.NewString())
	require.ErrorIs(t, err, ErrFailedPrecondition)
	_, err = client.GetSessionByID(t.Context(), request.Id)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRotatedBearerRenewsExpiredAncestryWithoutRevivingRevocation(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	root := authoritySession(t, client, nil, []string{"read"})
	child := authoritySession(t, client, root, []string{"read"})
	revoked := authoritySession(t, client, nil, []string{"read"})
	require.NoError(t, client.RevokeSessionAuthority(t.Context(), revoked.Id))
	past := time.Now().Add(-time.Minute)
	_, err := client.db.Exec(t.Context(), `UPDATE session_authority SET expires_at = $1 WHERE initiator_id = $2`, past, root.Creator)
	require.NoError(t, err)
	initial, err := client.CaptureDelegationCredential(t.Context(), root.Creator, 0, bytes.Repeat([]byte{1}, 64), past)
	require.NoError(t, err)
	require.True(t, initial)
	_, active, err := client.GetSessionAuthority(t.Context(), child.Id)
	require.NoError(t, err)
	require.False(t, active)
	future := time.Now().Add(time.Hour)
	saved, err := client.CaptureDelegationCredential(t.Context(), root.Creator, 1, bytes.Repeat([]byte{2}, 64), future)
	require.NoError(t, err)
	require.True(t, saved)
	for _, id := range []string{root.Id, child.Id} {
		grant, active, err := client.GetSessionAuthority(t.Context(), id)
		require.NoError(t, err)
		require.True(t, active)
		require.WithinDuration(t, future, grant.ExpiresAt.AsTime(), time.Microsecond)
	}
	_, active, err = client.GetSessionAuthority(t.Context(), revoked.Id)
	require.NoError(t, err)
	require.False(t, active)
	saved, err = client.CaptureDelegationCredential(t.Context(), root.Creator, 1, bytes.Repeat([]byte{3}, 64), future.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, saved)
	grant, _, err := client.GetSessionAuthority(t.Context(), root.Id)
	require.NoError(t, err)
	require.WithinDuration(t, future, grant.ExpiresAt.AsTime(), time.Microsecond)
	require.NoError(t, client.SaveDelegationCredential(t.Context(), root.Creator, bytes.Repeat([]byte{4}, 64)))
	grant, active, err = client.GetSessionAuthority(t.Context(), child.Id)
	require.NoError(t, err)
	require.True(t, active)
	require.Nil(t, grant.ExpiresAt)
}

func TestExpiredAuthorityStillPersistsFailedTaskBoundary(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	root := authoritySession(t, client, nil, []string{"read"})
	dispatch := uuid.New()
	require.NoError(t, client.ReserveSessionDispatch(t.Context(), root.Id, dispatch, "input", nil))
	task := newSessionTask(uuid.NewString(), "input")
	task.ContextID = root.Id
	version, err := client.CreateRuntimeTask(t.Context(), root.Id, taskMutationHash("initial"), task, dispatch.String())
	require.NoError(t, err)
	_, err = client.db.Exec(t.Context(), `UPDATE session_authority SET expires_at = $1 WHERE session_id = $2`, time.Now().Add(-time.Minute), root.Id)
	require.NoError(t, err)
	task.Status.State = a2a.TaskStateWorking
	_, err = client.UpdateSessionTask(t.Context(), root.Id, version, taskMutationHash("working"), task, task, dispatch.String())
	require.ErrorIs(t, err, ErrFailedPrecondition)
	task.Status.State = a2a.TaskStateFailed
	task.Status.Message = a2a.NewMessageForTask(a2a.MessageRoleAgent, task, a2a.NewTextPart("Execution authorization expired or was revoked."))
	event := &a2a.TaskStatusUpdateEvent{TaskID: task.ID, ContextID: task.ContextID, Status: task.Status}
	version, err = client.UpdateSessionTask(t.Context(), root.Id, version, taskMutationHash("failed"), task, event, dispatch.String())
	require.NoError(t, err)
	require.NoError(t, client.SettleSessionTask(t.Context(), root.Id, string(task.ID), version))
	stored, err := client.GetSessionTask(t.Context(), root.Id, string(task.ID), nil)
	require.NoError(t, err)
	require.Equal(t, a2a.TaskStateFailed, stored.Status.State)
	require.Equal(t, task.Status.Message, stored.Status.Message)
}
