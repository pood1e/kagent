package grpcserver

import (
	"context"
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.ate.dev/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type authorizationCheckpointTags struct{ tag *ateapipb.Tag }

func (c *authorizationCheckpointTags) GetTag(context.Context, string, string) (*ateapipb.Tag, error) {
	return c.tag, nil
}
func (*authorizationCheckpointTags) GetActor(context.Context, string, string) (*ateapipb.Actor, error) {
	return nil, status.Error(codes.NotFound, "source Actor deleted")
}
func (*authorizationCheckpointTags) CreateTag(context.Context, string, string, string) (*ateapipb.Tag, error) {
	return nil, status.Error(codes.Unimplemented, "unexpected tag creation")
}
func (*authorizationCheckpointTags) DeleteTag(context.Context, string, string, string) error {
	return status.Error(codes.Unimplemented, "unexpected tag deletion")
}

type admissionWriteStore struct {
	*database.Client
	pushes, dispatches int
}

func (s *admissionWriteStore) RegisterSessionPushNotification(ctx context.Context, sessionID, messageID, taskID string, config *a2a.PushConfig) error {
	s.pushes++
	return s.Client.RegisterSessionPushNotification(ctx, sessionID, messageID, taskID, config)
}
func (s *admissionWriteStore) ReserveSessionDispatch(ctx context.Context, id string, dispatch uuid.UUID, message string, origin *database.DispatchOrigin) error {
	s.dispatches++
	return s.Client.ReserveSessionDispatch(ctx, id, dispatch, message, origin)
}

type nativeAdmission struct {
	t                            *testing.T
	store                        *database.Client
	sessions                     *sessionsvc.Service
	source                       *apiv1.Session
	forks                        apiv1.CheckpointServiceClient
	schedules                    apiv1.ScheduledRunServiceClient
	scheduleID, scheduledSession string
	user                         context.Context
	local                        context.Context
	request                      *apiv1.ForkSessionRequest
}

func (a *nativeAdmission) prepare(provider auth.AuthProvider, token string, tags *authorizationCheckpointTags) {
	a.t.Helper()
	caller, err := provider.Authenticate(a.t.Context(), http.Header{"Authorization": {"Bearer " + token}}, nil)
	require.NoError(a.t, err)
	a.local = auth.AuthSessionTo(a.t.Context(), caller)
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("source"))
	message.ContextID = a.source.Id
	task := &a2a.Task{ID: a2a.TaskID(uuid.NewString()), ContextID: a.source.Id, History: []*a2a.Message{message}, Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}
	task.History[0].TaskID = task.ID
	dispatch := uuid.New()
	require.NoError(a.t, a.store.ReserveSessionDispatch(a.local, a.source.Id, dispatch, message.ID, nil))
	digest := sha256.Sum256([]byte(message.ID))
	version, err := a.store.CreateRuntimeTask(a.local, a.source.Id, digest[:], task, dispatch.String())
	require.NoError(a.t, err)
	require.NoError(a.t, a.store.SettleSessionTask(a.local, a.source.Id, string(task.ID), version))
	quiescence, err := a.store.ClaimSessionQuiescence(a.local)
	require.NoError(a.t, err)
	require.NoError(a.t, a.store.FinishSessionQuiescence(a.local, quiescence, &database.SessionTaskSnapshot{Atespace: "team", URI: "s3://snapshot/source", Fidelity: "SNAPSHOT_FIDELITY_VOLUMES"}))
	checkpoint, _, err := a.store.ReserveSessionCheckpoint(a.local, &apiv1.Checkpoint{Id: uuid.NewString(), SessionId: a.source.Id, HeadTaskId: string(task.ID)}, a.source.Creator, uuid.NewString())
	require.NoError(a.t, err)
	_, err = a.store.FinalizeSessionCheckpoint(a.local, checkpoint.Id, "tag-uid", "s3://snapshot/retained", "")
	require.NoError(a.t, err)
	tags.tag = &ateapipb.Tag{Metadata: &ateapipb.ResourceMetadata{Atespace: "team", Name: "checkpoint-" + checkpoint.Id, Uid: "tag-uid"}, Status: &ateapipb.TagStatus{Snapshot: &ateapipb.Snapshot{
		Fidelity:        ateapipb.SnapshotFidelity_SNAPSHOT_FIDELITY_VOLUMES,
		DurableSnapshot: &ateapipb.SnapshotStorage{Status: ateapipb.SnapshotStorageStatus_SNAPSHOT_STORAGE_STATUS_COMPLETED, Object: &ateapipb.ObjectSnapshot{SnapshotUri: "s3://snapshot/retained"}},
	}}}
	a.request = &apiv1.ForkSessionRequest{CheckpointId: checkpoint.Id, RequestId: uuid.NewString()}
	scheduled, err := a.schedules.CreateScheduledRun(a.user, &apiv1.CreateScheduledRunRequest{Agent: a.source.Agent, RequestId: uuid.NewString(), Config: &apiv1.ScheduledRunConfig{Schedule: "* * * * *", Paused: true, Prompt: "automated"}})
	require.NoError(a.t, err)
	a.scheduleID = scheduled.ScheduledRun.Id
	firing, err := a.schedules.TriggerScheduledRun(a.user, &apiv1.TriggerScheduledRunRequest{ScheduledRunId: a.scheduleID, RequestId: uuid.NewString()})
	require.NoError(a.t, err)
	linked, err := a.store.ReserveScheduledRunExecutionSession(a.local, uuid.MustParse(firing.Execution.Id), a.source.Creator)
	require.NoError(a.t, err)
	a.scheduledSession = linked.SessionId
	retained, _, err := a.store.GetSessionAuthority(a.local, linked.SessionId)
	require.NoError(a.t, err)
	require.True(a.t, proto.Equal(a.source.Authority.ScopeCeiling.Resources, retained.ScopeCeiling.Resources))
}

func (a *nativeAdmission) check(allowed bool) {
	a.t.Helper()
	writes := &admissionWriteStore{Client: a.store}
	interactions := sessionsvc.NewInteractionService(writes, nil, a.sessions, false)
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("continue"))
	message.ContextID = a.source.Id
	prepared, err := interactions.PrepareSend(a.local, types.NamespacedName{Namespace: "team", Name: "runner"}, &a2a.SendMessageRequest{Message: message, Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{URL: "https://notify.example/callback"}}})
	if allowed {
		require.NoError(a.t, err)
		revoked, err := a.store.RevokeSessionDispatch(a.local, a.source.Id, prepared.DispatchID, message.ID)
		require.NoError(a.t, err)
		require.True(a.t, revoked)
		require.Equal(a.t, 1, writes.pushes)
		require.Equal(a.t, 1, writes.dispatches)
	} else {
		require.ErrorIs(a.t, err, a2a.ErrUnauthorized)
		require.Nil(a.t, prepared)
		require.Zero(a.t, writes.pushes)
		require.Zero(a.t, writes.dispatches)
	}
	_, err = a.sessions.ExecutionContext(a.t.Context(), a.source.Id)
	if allowed {
		require.NoError(a.t, err)
	} else {
		require.Error(a.t, err)
	}
	_, err = a.sessions.ProvisioningContext(a.t.Context(), a.scheduledSession)
	if allowed {
		require.NoError(a.t, err)
	} else {
		require.Error(a.t, err, "automation stops before runtime provisioning")
		_, err = a.schedules.TriggerScheduledRun(a.user, &apiv1.TriggerScheduledRunRequest{ScheduledRunId: a.scheduleID, RequestId: uuid.NewString()})
		assertGRPCCode(a.t, err, codes.PermissionDenied)
	}
	result, err := a.forks.ForkSession(a.user, a.request)
	if !allowed {
		assertGRPCCode(a.t, err, codes.PermissionDenied)
		return
	}
	require.NoError(a.t, err)
	require.True(a.t, proto.Equal(a.source.Authority.ScopeCeiling.Resources, result.Session.Authority.ScopeCeiling.Resources), "fork and retry keep the source's exact capability ceiling")
	require.Empty(a.t, result.Session.Authority.ParentSessionId)
	require.Equal(a.t, result.Session.Id, result.Session.Authority.RootSessionId)
}
