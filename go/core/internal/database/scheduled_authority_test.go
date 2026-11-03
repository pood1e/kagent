package database

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func TestScheduledAuthoritySnapshotsSurviveEditsAndDeletion(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	schedule, _ := createTestSchedule(t, client)
	id := uuid.MustParse(schedule.Id)
	initial, err := client.GetScheduledRunAuthority(t.Context(), id, schedule.Creator)
	require.NoError(t, err)
	_, err = client.GetScheduledRunAuthority(t.Context(), id, "other")
	require.ErrorIs(t, err, ErrNotFound)
	first, err := client.TriggerScheduledRun(t.Context(), id, schedule.Creator, "first")
	require.NoError(t, err)
	widened := proto.CloneOf(initial)
	widened.ScopeCeiling.Resources.Grants = append(widened.ScopeCeiling.Resources.Grants, fixedToolGrant("Read"))
	_, err = client.UpdateScheduledRun(t.Context(), id, schedule.Creator, schedule.Etag, schedule.Config, widened)
	require.NoError(t, err)
	second, err := client.TriggerScheduledRun(t.Context(), id, schedule.Creator, "second")
	require.NoError(t, err)
	_, err = client.DeleteScheduledRun(t.Context(), id, schedule.Creator)
	require.NoError(t, err)
	for _, test := range []struct {
		execution *apiv1.ScheduledRunExecution
		want      *apiv1.SessionAuthority
	}{{first, initial}, {second, widened}} {
		linked, err := client.ReserveScheduledRunExecutionSession(t.Context(), uuid.MustParse(test.execution.Id), schedule.Creator)
		require.NoError(t, err)
		actual, active, err := client.GetSessionAuthority(t.Context(), linked.SessionId)
		require.NoError(t, err)
		require.True(t, active)
		expected, err := auth.CanonicalResourceGrants(test.want.ScopeCeiling.Resources)
		require.NoError(t, err)
		require.True(t, proto.Equal(expected, actual.ScopeCeiling.Resources), "accepted firings retain their own resource selection")
		require.Equal(t, linked.SessionId, actual.RootSessionId)
		require.Empty(t, actual.ParentSessionId)
		require.Equal(t, initial.InitiatorId, actual.InitiatorId)
	}
}

func TestScheduledAuthorityRejectsUnboundedOrMalformedExecution(t *testing.T) {
	client := NewClient(setupTestDB(t), "public")
	schedule, hash := createTestSchedule(t, client)
	_, err := client.CreateScheduledRun(t.Context(), schedule, "unbounded", hash, nil)
	require.ErrorIs(t, err, ErrFailedPrecondition)
	_, err = client.FindScheduledRunRequest(t.Context(), schedule.Creator, "unbounded", hash)
	require.ErrorIs(t, err, ErrNotFound)
	grant := scheduleTestAuthority()
	grant.InitiatorId = "other"
	_, err = client.CreateScheduledRun(t.Context(), schedule, "wrong-initiator", hash, grant)
	require.ErrorIs(t, err, ErrFailedPrecondition)
	for _, data := range [][]byte{nil, {0xff}} {
		firing, err := client.TriggerScheduledRun(t.Context(), uuid.MustParse(schedule.Id), schedule.Creator, uuid.NewString())
		require.NoError(t, err)
		_, err = client.db.Exec(t.Context(), `UPDATE scheduled_run_execution SET authority=$2 WHERE id=$1`, firing.Id, data)
		require.NoError(t, err)
		_, err = client.ReserveScheduledRunExecutionSession(t.Context(), uuid.MustParse(firing.Id), schedule.Creator)
		require.Error(t, err)
		actual, err := client.GetScheduledRunExecution(t.Context(), uuid.MustParse(firing.Id), schedule.Creator)
		require.NoError(t, err)
		require.Empty(t, actual.SessionId, "failed admission leaves no Session receipt")
	}
}
