package grpcserver

import (
	"context"

	"github.com/kagent-dev/kagent/go/core/internal/database"
)

func (s *memoryRPCStore) DeleteMemoryEntry(_ context.Context, agent, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	for i, row := range s.rows[agent] {
		if row.ID == id {
			s.rows[agent] = append(s.rows[agent][:i], s.rows[agent][i+1:]...)
			return nil
		}
	}
	return database.ErrNotFound
}
