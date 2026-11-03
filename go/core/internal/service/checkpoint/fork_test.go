package checkpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (*testStore) GetCheckpointExecution(context.Context, string, string) (*database.CheckpointExecution, error) {
	return &database.CheckpointExecution{}, nil
}

type testForkAuthorizer struct {
	bindErr  error
	checkErr error
	checks   int
}

func (a *testForkAuthorizer) BindForkAuthority(_ context.Context, id, creator string, _ *database.CheckpointExecution) (*apiv1.SessionAuthority, error) {
	return &apiv1.SessionAuthority{SessionId: id, InitiatorId: creator}, a.bindErr
}

func (a *testForkAuthorizer) CheckForkAuthority(context.Context, *apiv1.Session, *apiv1.SessionAuthority) error {
	a.checks++
	return a.checkErr
}

func forkTestFixture(t *testing.T, authorizer ForkAuthorizer) (*Service, *testStore, *testWorkflow) {
	t.Helper()
	checkpoint := &apiv1.Checkpoint{Id: "018f47a2-4efb-7c21-a848-123456789abc", SessionId: "11111111-1111-4111-8111-111111111111", State: apiv1.CheckpointState_CHECKPOINT_STATE_READY}
	store := &testStore{prepared: checkpoint, tagUID: "tag-uid", snapshot: &database.SessionTaskSnapshot{Atespace: "team-a", URI: "s3://snapshots/snapshot-1", Fidelity: "SNAPSHOT_FIDELITY_VOLUMES"}}
	workflow := &testWorkflow{}
	tags := &testTags{created: &ateapipb.Tag{
		Metadata: &ateapipb.ResourceMetadata{Atespace: "team-a", Name: tagName(checkpoint.Id), Uid: store.tagUID},
		Status:   &ateapipb.TagStatus{Snapshot: &ateapipb.Snapshot{Generation: 1, Fidelity: ateapipb.SnapshotFidelity_SNAPSHOT_FIDELITY_VOLUMES, ActorTemplateUid: "template-uid", DurableSnapshot: &ateapipb.SnapshotStorage{State: ateapipb.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED, Object: &ateapipb.ObjectSnapshot{SnapshotUri: store.snapshot.URI}}}},
	}}
	return NewService(store, testAuthorizer{}, tags, workflow, authorizer), store, workflow
}

func TestForkAdmissionPrecedesReservationAndProvisioning(t *testing.T) {
	for _, test := range []struct {
		name       string
		authorizer *testForkAuthorizer
		reserved   bool
	}{
		{"dependency revoked", &testForkAuthorizer{bindErr: serviceerrors.NewPermissionDenied("dependency revoked", nil)}, false},
		{"engine unavailable", &testForkAuthorizer{bindErr: serviceerrors.NewUnavailable("OpenFGA unavailable", nil)}, false},
		{"creation receipt denied", &testForkAuthorizer{checkErr: serviceerrors.NewPermissionDenied("receipt bounds changed", nil)}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store, workflow := forkTestFixture(t, test.authorizer)
			_, err := service.Fork(auth.AuthSessionTo(t.Context(), testSession{userID: "alice"}), store.prepared.Id, "request")
			require.Error(t, err)
			require.Equal(t, test.reserved, store.forked != nil)
			require.Nil(t, workflow.session, "a denied fork never starts a runtime")
		})
	}
	service, store, workflow := forkTestFixture(t, nil)
	_, err := service.Fork(auth.AuthSessionTo(t.Context(), testSession{userID: "alice"}), store.prepared.Id, "request")
	require.Equal(t, serviceerrors.CodeUnavailable, serviceerrors.CodeOf(err))
	require.Nil(t, store.forked)
	require.Nil(t, workflow.session)
}

func TestForkRechecksAnExistingCreationReceipt(t *testing.T) {
	authorizer := &testForkAuthorizer{}
	service, store, workflow := forkTestFixture(t, authorizer)
	ctx := auth.AuthSessionTo(t.Context(), testSession{userID: "alice"})
	first, err := service.Fork(ctx, store.prepared.Id, "request")
	require.NoError(t, err)
	require.Equal(t, 1, authorizer.checks)
	workflow.session = nil
	authorizer.checkErr = serviceerrors.NewPermissionDenied("dependency revoked", nil)
	_, err = service.Fork(ctx, store.prepared.Id, "request")
	require.Equal(t, serviceerrors.CodePermissionDenied, serviceerrors.CodeOf(err))
	require.Same(t, first, store.forked)
	require.Equal(t, 2, authorizer.checks)
	require.Nil(t, workflow.session, "a stored receipt cannot bypass current checks")
}
