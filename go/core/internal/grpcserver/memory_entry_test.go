package grpcserver

import (
	"context"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
)

func (s *memoryRPCStore) DeleteMemoryEntry(_ context.Context, scope *runtimev1.MemoryScope, user, id string) error {
	key := memoryPartition(scope, user)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	for i, row := range s.rows[key] {
		if row.ID == id {
			s.rows[key] = append(s.rows[key][:i], s.rows[key][i+1:]...)
			return nil
		}
	}
	return database.ErrNotFound
}
