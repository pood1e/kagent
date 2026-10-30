package database

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContinuationStateRetainsItsAnchorAcrossNativeResumes(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	parent := continuationSession(t, client)
	child := continuationSession(t, client, parent)
	anchor := continuationTask(t, client, parent, "parent")
	latest, pending, err := client.GetTaskContinuationState(t.Context(), parent.Id, string(anchor.ID))
	require.NoError(t, err)
	require.False(t, pending)
	require.Equal(t, string(anchor.ID), latest.TaskId)
	require.NoError(t, client.RegisterTaskContinuation(t.Context(), parent.Id, child.Id, "child"))
	_, pending, err = client.GetTaskContinuationState(t.Context(), parent.Id, string(anchor.ID))
	require.NoError(t, err)
	require.True(t, pending)
	callee := continuationTask(t, client, child, "child", &DispatchOrigin{SessionID: parent.Id})
	settleContinuation(t, client, callee, a2a.TaskStateInputRequired)
	settleContinuation(t, client, anchor, a2a.TaskStateCompleted)
	reply := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("approved"))
	reply.ID, reply.ContextID, reply.TaskID = "approved", child.Id, callee.ID
	callee, _ = resumeRuntimeTask(t, client, child.Id, reply)
	settleContinuation(t, client, callee, a2a.TaskStateCompleted)
	claims, err := client.ClaimTaskContinuations(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	// Admission and acknowledgement are separate durable phases. Observation
	// keeps waiting while the continuation's lease owns an ambiguous send.
	resumed := continuationTask(t, client, parent, "kagent-continuation-"+claims[0].ID.String())
	latest, pending, err = client.GetTaskContinuationState(t.Context(), parent.Id, string(anchor.ID))
	require.NoError(t, err)
	require.True(t, pending)
	require.Equal(t, string(anchor.ID), latest.TaskId)
	require.NoError(t, client.CompleteTaskContinuation(t.Context(), claims[0], string(resumed.ID)))
	latest, pending, err = client.GetTaskContinuationState(t.Context(), parent.Id, string(anchor.ID))
	require.NoError(t, err)
	require.False(t, pending)
	require.Equal(t, string(resumed.ID), latest.TaskId)
	require.Equal(t, parent.Id, latest.SessionId)
	_, _, err = client.GetTaskContinuationState(t.Context(), child.Id, string(anchor.ID))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFailedOwnerCancelsContinuationBelowACompletedForegroundTask(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "revision", "assistant", "kagent")
	rootSession := continuationSession(t, client)
	child := continuationSession(t, client, rootSession)
	leafSession := continuationSession(t, client, child)
	root := continuationTask(t, client, rootSession, "root")
	anchor := continuationTask(t, client, child, "child", &DispatchOrigin{SessionID: rootSession.Id})
	require.NoError(t, client.RegisterTaskContinuation(t.Context(), child.Id, leafSession.Id, "leaf"))
	leaf := continuationTask(t, client, leafSession, "leaf", &DispatchOrigin{SessionID: child.Id})
	settleContinuation(t, client, leaf, a2a.TaskStateInputRequired)
	settleContinuation(t, client, anchor, a2a.TaskStateCompleted)
	settleContinuation(t, client, root, a2a.TaskStateFailed)
	claims, err := client.ClaimTaskCancellations(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, string(leaf.ID), claims[0].TaskID)
	_, pending, err := client.GetTaskContinuationState(t.Context(), child.Id, string(anchor.ID))
	require.NoError(t, err)
	require.False(t, pending)
	reply := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("late approval"))
	reply.ID, reply.ContextID, reply.TaskID = "late", leafSession.Id, leaf.ID
	dispatch := uuid.New()
	require.NoError(t, client.ReserveSessionDispatch(t.Context(), leafSession.Id, dispatch, reply.ID, nil))
	stored, version, err := client.GetVersionedSessionTask(t.Context(), leafSession.Id, string(leaf.ID))
	require.NoError(t, err)
	stored.History = append(stored.History, reply)
	stored.Status.State = a2a.TaskStateWorking
	_, err = client.UpdateSessionTask(t.Context(), leafSession.Id, version, taskMutationHash("late"), stored, stored, dispatch.String())
	require.ErrorIs(t, err, ErrFailedPrecondition)
}
