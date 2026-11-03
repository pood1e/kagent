package database

import (
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func checkpointExecutionFixture(t *testing.T) (*Client, *apiv1.Session, *apiv1.Checkpoint) {
	t.Helper()
	client := NewClient(setupTestDB(t), "public")
	sessionFixture(t, client, t.Context(), "team-a", "checkpoint-revision", "assistant", "kagent")
	request := newSessionRequest(uuid.NewString(), "assistant", "kagent", "source")
	request.Authority = &apiv1.SessionAuthority{SessionId: request.Id, InitiatorId: request.Creator, RootSessionId: request.Id, WorkspaceUid: "workspace-1",
		ScopeCeiling: &apiv1.ScopeCeiling{Scopes: []string{"read"}, Resources: &apiv1.ResourceGrantSet{Grants: []*apiv1.ResourceGrant{fixedToolGrant("Read")}}}, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}
	source, _, err := client.CreateSession(t.Context(), request, "source")
	require.NoError(t, err)
	source, err = markSessionReady(t.Context(), client, source.Id, "actor.example")
	require.NoError(t, err)
	task := newSessionTask(uuid.NewString(), "input")
	task.ContextID, task.Status.State = source.Id, a2a.TaskStateCompleted
	require.NoError(t, saveRuntimeTask(t, client, source.Id, task, task, &SessionTaskSnapshot{Atespace: "team-a", URI: "s3://snapshot/source", Fidelity: "SNAPSHOT_FIDELITY_VOLUMES"}))
	checkpoint, _, err := client.ReserveSessionCheckpoint(t.Context(), &apiv1.Checkpoint{Id: uuid.NewString(), SessionId: source.Id, HeadTaskId: string(task.ID)}, source.Creator, "checkpoint")
	require.NoError(t, err)
	checkpoint, err = client.FinalizeSessionCheckpoint(t.Context(), checkpoint.Id, "tag-uid", "s3://snapshot/retained", "")
	require.NoError(t, err)
	return client, source, checkpoint
}

func TestCheckpointExecutionRetainsOwnedImmutableBounds(t *testing.T) {
	client, source, checkpoint := checkpointExecutionFixture(t)
	for _, owner := range []string{"other", ""} {
		_, err := client.GetCheckpointExecution(t.Context(), checkpoint.Id, owner)
		require.ErrorIs(t, err, ErrNotFound)
	}
	retained, err := client.GetCheckpointExecution(t.Context(), checkpoint.Id, source.Creator)
	require.NoError(t, err)
	require.Equal(t, source.PreparedRevision, retained.Revision.Revision)
	require.True(t, proto.Equal(source.Authority.ScopeCeiling, retained.Authority.ScopeCeiling))
	require.NoError(t, client.RevokeSessionAuthority(t.Context(), source.Id))
	require.NoError(t, deleteSession(t.Context(), client, source.Id))
	after, err := client.GetCheckpointExecution(t.Context(), checkpoint.Id, source.Creator)
	require.NoError(t, err)
	require.True(t, proto.Equal(retained.Authority, after.Authority), "retained bounds survive source runtime deletion and revocation")
	_, _, err = client.BeginDeleteSessionCheckpoint(t.Context(), checkpoint.Id, source.Creator)
	require.NoError(t, err)
	_, err = client.GetCheckpointExecution(t.Context(), checkpoint.Id, source.Creator)
	require.ErrorIs(t, err, ErrNotFound, "only READY checkpoints admit new execution")
}

func TestForkTransactionFencesRetainedResourceBounds(t *testing.T) {
	client, source, checkpoint := checkpointExecutionFixture(t)
	retained, err := client.GetCheckpointExecution(t.Context(), checkpoint.Id, source.Creator)
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		change func(*apiv1.SessionAuthority)
	}{
		{"new tool", func(grant *apiv1.SessionAuthority) {
			grant.ScopeCeiling.Resources.Grants = append(grant.ScopeCeiling.Resources.Grants, fixedToolGrant("delete"))
		}},
		{"recreated server", func(grant *apiv1.SessionAuthority) {
			grant.ScopeCeiling.Resources.Grants[0].Resource.GetMcpTool().Server.Uid = "replacement"
		}},
		{"wider scope", func(grant *apiv1.SessionAuthority) {
			grant.ScopeCeiling.Scopes = append(grant.ScopeCeiling.Scopes, "write")
		}},
		{"different workspace", func(grant *apiv1.SessionAuthority) { grant.WorkspaceUid = "other" }},
		{"different initiator", func(grant *apiv1.SessionAuthority) { grant.InitiatorId = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, requestID := uuid.NewString(), uuid.NewString()
			grant := proto.CloneOf(retained.Authority)
			grant.SessionId, grant.RootSessionId, grant.ParentSessionId = id, id, ""
			test.change(grant)
			_, _, err := client.ForkSession(t.Context(), checkpoint.Id, source.Creator, requestID, id, grant)
			require.ErrorIs(t, err, ErrFailedPrecondition)
			_, err = client.GetSessionByID(t.Context(), id)
			require.ErrorIs(t, err, ErrNotFound)
			_, err = client.GetSessionByRequestID(t.Context(), source.Creator, requestID)
			require.ErrorIs(t, err, ErrNotFound, "denial rolls back the fork and its receipt")
		})
	}
	grant := proto.CloneOf(retained.Authority)
	grant.SessionId, grant.RootSessionId = uuid.NewString(), ""
	grant.RootSessionId = grant.SessionId
	grant.ExpiresAt = timestamppb.New(time.Now().Add(2 * time.Hour).Truncate(time.Microsecond))
	fork, created, err := client.ForkSession(t.Context(), checkpoint.Id, source.Creator, "fresh-fork", grant.SessionId, grant)
	require.NoError(t, err)
	require.True(t, created)
	actual, active, err := client.GetSessionAuthority(t.Context(), fork.Id)
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, proto.Equal(grant, actual), "fresh authority preserves bounds with an independent lease")
}

func TestCheckpointExecutionRejectsCorruptSourceCeiling(t *testing.T) {
	client, source, checkpoint := checkpointExecutionFixture(t)
	_, err := client.db.Exec(t.Context(), `UPDATE session_authority SET resource_grants=$2 WHERE session_id=$1`, source.Id, []byte{0xff})
	require.NoError(t, err)
	_, err = client.GetCheckpointExecution(t.Context(), checkpoint.Id, source.Creator)
	require.Error(t, err)
}
