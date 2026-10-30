package database

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTerminalTaskCancelsUnfinishedForegroundSubtrees(t *testing.T) {
	for _, state := range []a2a.TaskState{a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateCanceled} {
		t.Run(string(state), func(t *testing.T) {
			pool := setupTestDB(t)
			client := NewClient(pool, "public")
			sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
			parent := continuationSession(t, client)
			child := continuationSession(t, client, parent)
			nested := continuationSession(t, client, child)
			background := continuationSession(t, client, parent)
			root := continuationTask(t, client, parent, "root")
			callee := continuationTask(t, client, child, "foreground", &DispatchOrigin{SessionID: parent.Id})
			leaf := continuationTask(t, client, nested, "nested-background", &DispatchOrigin{SessionID: child.Id, Background: true})
			other := continuationTask(t, client, background, "background", &DispatchOrigin{SessionID: parent.Id, Background: true})
			settleContinuation(t, client, root, state)
			claims, err := NewClient(pool, "public").ClaimTaskCancellations(t.Context(), 10)
			require.NoError(t, err)
			ids := make([]string, 0, len(claims))
			for _, claim := range claims {
				ids = append(ids, claim.TaskID)
				require.NoError(t, client.CompleteTaskCancellation(t.Context(), claim))
			}
			require.ElementsMatch(t, []string{string(callee.ID), string(leaf.ID)}, ids)
			stored, _, err := client.GetVersionedSessionTask(t.Context(), background.Id, string(other.ID))
			require.NoError(t, err)
			require.Equal(t, a2a.TaskStateWorking, stored.Status.State)
		})
	}
}

func TestTerminalTaskFencesReservedForegroundAdmission(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	for range 10 {
		parent := continuationSession(t, client)
		child := continuationSession(t, client, parent)
		root := continuationTask(t, client, parent, "root")
		dispatch := uuid.New()
		require.NoError(t, client.ReserveSessionDispatch(t.Context(), child.Id, dispatch, "child", &DispatchOrigin{SessionID: parent.Id}))
		root.Status.State = a2a.TaskStateFailed
		_, version, err := client.GetVersionedSessionTask(t.Context(), parent.Id, string(root.ID))
		require.NoError(t, err)
		version, err = client.UpdateSessionTask(t.Context(), parent.Id, version, taskMutationHash("failed"), root, root, "")
		require.NoError(t, err)
		callee := newSessionTask(uuid.NewString(), "child")
		callee.ContextID = child.Id
		start := make(chan struct{})
		created, settled := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := client.CreateRuntimeTask(t.Context(), child.Id, taskMutationHash("child"), callee, dispatch.String())
			created <- err
		}()
		go func() {
			<-start
			settled <- client.SettleSessionTask(t.Context(), parent.Id, string(root.ID), version)
		}()
		close(start)
		err = <-created
		require.NoError(t, <-settled)
		claims, claimErr := client.ClaimTaskCancellations(t.Context(), 10)
		require.NoError(t, claimErr)
		if err != nil {
			require.ErrorIs(t, err, ErrFailedPrecondition)
			require.Empty(t, claims)
			_, err = client.GetTaskExecution(t.Context(), string(callee.ID))
			require.ErrorIs(t, err, ErrNotFound)
		} else {
			require.Len(t, claims, 1)
			require.Equal(t, string(callee.ID), claims[0].TaskID)
			require.NoError(t, client.CompleteTaskCancellation(t.Context(), claims[0]))
		}
	}
}

func TestCompletedOwnerPreservesBackgroundWorkOfFinishedDelegates(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	parent := continuationSession(t, client)
	child := continuationSession(t, client, parent)
	background := continuationSession(t, client, child)
	root := continuationTask(t, client, parent, "root")
	callee := continuationTask(t, client, child, "foreground", &DispatchOrigin{SessionID: parent.Id})
	continuationTask(t, client, background, "background", &DispatchOrigin{SessionID: child.Id, Background: true})
	settleContinuation(t, client, callee, a2a.TaskStateCompleted)
	settleContinuation(t, client, root, a2a.TaskStateCompleted)
	claims, err := client.ClaimTaskCancellations(t.Context(), 10)
	require.NoError(t, err)
	require.Empty(t, claims)
}
