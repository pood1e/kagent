package grpcserver

import (
	"context"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/inventory"
)

type inventoryServer struct {
	runtimev1.UnimplementedToolInventoryServiceServer
	service *inventory.Service
}

func (s *inventoryServer) Report(ctx context.Context, request *runtimev1.ToolInventoryServiceReportRequest) (*runtimev1.ToolInventoryServiceReportResponse, error) {
	if err := s.service.Report(ctx, request); err != nil {
		return nil, err
	}
	return &runtimev1.ToolInventoryServiceReportResponse{}, nil
}
