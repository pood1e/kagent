package checkpoint

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"go.ate.dev/substrate/pkg/proto/ateapipb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/internal/substrate"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// ForkAuthorizer binds checkpoint restores to current initiator permissions and
// the retained resource ceiling, then rechecks creation receipts before launch.
type ForkAuthorizer interface {
	BindForkAuthority(context.Context, string, string, *database.CheckpointExecution) (*apiv1.SessionAuthority, error)
	CheckForkAuthority(context.Context, *apiv1.Session, *apiv1.SessionAuthority) error
}

func (s *Service) Fork(ctx context.Context, checkpointID, requestID string) (*apiv1.Session, error) {
	if err := validateCreate(checkpointID, requestID); err != nil {
		return nil, err
	}
	userID, err := s.authorize(ctx, auth.VerbCreate, "Session", "")
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to generate Session identifier", err)
	}
	if s.forks == nil {
		return nil, serviceerrors.NewUnavailable("Checkpoint execution authorization is not configured", nil)
	}
	source, err := s.store.GetCheckpointExecution(ctx, checkpointID, userID)
	if errors.Is(err, database.ErrNotFound) {
		return nil, serviceerrors.NewNotFound("Checkpoint execution not found", err)
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Cannot resolve checkpoint execution", err)
	}
	grant, err := s.forks.BindForkAuthority(ctx, id.String(), userID, source)
	if err != nil {
		return nil, err
	}

	snapshot, tagUID, err := s.store.GetSessionCheckpointSnapshot(ctx, checkpointID, userID)
	if errors.Is(err, database.ErrNotFound) {
		return nil, serviceerrors.NewNotFound("Checkpoint not found", err)
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to get checkpoint", err)
	}
	if snapshot.Fidelity != "SNAPSHOT_FIDELITY_VOLUMES" {
		return nil, serviceerrors.NewFailedPrecondition("Only volume checkpoints can be forked", nil)
	}
	tag, err := s.tags.GetTag(ctx, snapshot.Atespace, tagName(checkpointID))
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Failed to get checkpoint tag", err)
	}
	if tagUID == "" || tag.GetMetadata().GetUid() != tagUID ||
		tag.GetMetadata().GetAtespace() != snapshot.Atespace || tag.GetMetadata().GetName() != tagName(checkpointID) ||
		snapshot.URI == "" || substrate.SnapshotURI(tag.GetStatus().GetSnapshot()) != snapshot.URI ||
		tag.GetStatus().GetSnapshot().GetFidelity() != ateapipb.SnapshotFidelity_SNAPSHOT_FIDELITY_VOLUMES {
		return nil, serviceerrors.NewFailedPrecondition("Checkpoint tag identity changed", nil)
	}
	session, _, err := s.store.ForkSession(ctx, checkpointID, userID, requestID, id.String(), grant)
	if errors.Is(err, database.ErrIdempotencyConflict) {
		return nil, serviceerrors.NewAlreadyExists("request_id was already used for a different Session", err)
	}
	if errors.Is(err, database.ErrFailedPrecondition) {
		return nil, serviceerrors.NewFailedPrecondition("Checkpoint fork preconditions changed", err)
	}
	if errors.Is(err, database.ErrNotFound) {
		return nil, serviceerrors.NewNotFound("Checkpoint not found", err)
	}
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to reserve fork Session", err)
	}
	if err := s.forks.CheckForkAuthority(ctx, session, grant); err != nil {
		if errors.Is(err, database.ErrIdempotencyConflict) {
			return nil, serviceerrors.NewAlreadyExists("Fork request belongs to another execution", err)
		}
		return nil, err
	}
	session, err = s.workflow.Create(ctx, session)
	if errors.Is(err, database.ErrConflict) {
		return nil, serviceerrors.NewAborted(err.Error(), err)
	}
	if errors.Is(err, database.ErrNotFound) {
		return nil, serviceerrors.NewNotFound("Session was deleted", err)
	}
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Failed to create fork Session", err)
	}
	return session, nil
}
