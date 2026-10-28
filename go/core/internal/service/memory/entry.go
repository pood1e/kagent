package memory

import (
	"context"
	"errors"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// DeleteEntry removes one memory after resolving the caller's agent and user scope.
func (s *Service) DeleteEntry(ctx context.Context, requested *runtimev1.MemoryScope, id string) error {
	scope, userID, err := s.managementScope(ctx, auth.VerbDelete, requested)
	if err != nil {
		return err
	}
	if scope == nil || id == "" {
		return serviceerrors.NewInvalidArgument("agent and memory ID are required", nil)
	}
	if s.store == nil {
		return serviceerrors.NewInternal("memory store is not configured", nil)
	}
	err = s.store.DeleteMemoryEntry(ctx, scope, userID, id)
	if errors.Is(err, database.ErrNotFound) {
		return serviceerrors.NewNotFound("memory entry not found", err)
	}
	if err != nil {
		return serviceerrors.NewInternal("failed to delete memory entry", err)
	}
	return nil
}
