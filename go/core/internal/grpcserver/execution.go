package grpcserver

import (
	"context"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/execution"
)

type executionServer struct {
	apiv1.UnimplementedExecutionServiceServer
	service *execution.Service
}

func (s *executionServer) GetTaskExecution(ctx context.Context, request *apiv1.GetTaskExecutionRequest) (*apiv1.GetTaskExecutionResponse, error) {
	result, err := s.service.Get(ctx, request.TaskId)
	if err != nil {
		return nil, err
	}
	return &apiv1.GetTaskExecutionResponse{Execution: result}, nil
}
func (s *executionServer) ListTaskExecutions(ctx context.Context, request *apiv1.ListTaskExecutionsRequest) (*apiv1.ListTaskExecutionsResponse, error) {
	return s.service.List(ctx, request)
}
