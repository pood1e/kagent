package grpcserver

import (
	"context"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/taskcatalog"
)

type taskCatalogServer struct {
	apiv1.UnimplementedTaskCatalogServiceServer
	service *taskcatalog.Service
}

var _ apiv1.TaskCatalogServiceServer = (*taskCatalogServer)(nil)

func (s *taskCatalogServer) ListTaskGroups(ctx context.Context, r *apiv1.ListTaskGroupsRequest) (*apiv1.ListTaskGroupsResponse, error) {
	return s.service.ListTaskGroups(ctx, r)
}
func (s *taskCatalogServer) ListTaskRecords(ctx context.Context, r *apiv1.ListTaskRecordsRequest) (*apiv1.ListTaskRecordsResponse, error) {
	return s.service.ListTaskRecords(ctx, r)
}
func (s *taskCatalogServer) ListSessionActivity(ctx context.Context, r *apiv1.ListSessionActivityRequest) (*apiv1.ListSessionActivityResponse, error) {
	return s.service.ListSessionActivity(ctx, r)
}
func (s *taskCatalogServer) ListAgentActivity(ctx context.Context, r *apiv1.ListAgentActivityRequest) (*apiv1.ListAgentActivityResponse, error) {
	return s.service.ListAgentActivity(ctx, r)
}
