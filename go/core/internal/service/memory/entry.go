package memory

import (
	"context"
	"errors"

	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// DeleteEntry removes one memory of the Agent the caller may change.
func (s *Service) DeleteEntry(ctx context.Context, agentName, id string) error {
	agentName, _, err := s.memoryScope(ctx, auth.VerbDelete, agentName)
	if err != nil {
		return err
	}
	if id == "" {
		return serviceerrors.NewInvalidArgument("memory ID is required", nil)
	}
	if s.store == nil {
		return serviceerrors.NewInternal("memory store is not configured", nil)
	}
	err = s.store.DeleteMemoryEntry(ctx, agentName, id)
	if errors.Is(err, database.ErrNotFound) {
		return serviceerrors.NewNotFound("memory entry not found", err)
	}
	if err != nil {
		return serviceerrors.NewInternal("failed to delete memory entry", err)
	}
	return nil
}
