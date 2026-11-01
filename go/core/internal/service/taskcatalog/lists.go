package taskcatalog

import (
	"context"
	"math"
	"time"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func (s *Service) ListTaskGroups(ctx context.Context, request *apiv1.ListTaskGroupsRequest) (*apiv1.ListTaskGroupsResponse, error) {
	if request == nil {
		return nil, serviceerrors.NewInvalidArgument("A task-group request is required", nil)
	}
	q, binding, err := s.prepare(ctx, catalogInput{kind: "groups", message: request, scope: request.Scope, page: request.Page, agent: request.Agent, activities: request.Activities, boundaries: request.BoundaryAgents, view: request.View, query: request.Query})
	if err != nil {
		return nil, err
	}
	page, err := s.store.ListCatalogTaskGroups(ctx, q)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to list task groups", err)
	}
	metadata, total, err := pageMetadata(binding, page.NextID, page.NextTime, page.Total)
	if err != nil {
		return nil, err
	}
	return &apiv1.ListTaskGroupsResponse{Groups: page.Groups, Tasks: page.Tasks, Sessions: page.Sessions, TotalSize: total, Page: metadata}, nil
}

func (s *Service) ListTaskRecords(ctx context.Context, request *apiv1.ListTaskRecordsRequest) (*apiv1.ListTaskRecordsResponse, error) {
	if request == nil {
		return nil, serviceerrors.NewInvalidArgument("A task-record request is required", nil)
	}
	q, binding, err := s.prepare(ctx, catalogInput{kind: "records", message: request, scope: request.Scope, page: request.Page, agent: request.Agent, activities: request.Activities, boundaries: request.BoundaryAgents, view: request.View})
	if err != nil {
		return nil, err
	}
	page, err := s.store.ListCatalogTaskRecords(ctx, q)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to list task records", err)
	}
	metadata, total, err := pageMetadata(binding, page.NextID, page.NextTime, page.Total)
	if err != nil {
		return nil, err
	}
	return &apiv1.ListTaskRecordsResponse{Tasks: page.Tasks, Sessions: page.Sessions, TotalSize: total, Page: metadata}, nil
}

func (s *Service) ListSessionActivity(ctx context.Context, request *apiv1.ListSessionActivityRequest) (*apiv1.ListSessionActivityResponse, error) {
	if request == nil {
		return nil, serviceerrors.NewInvalidArgument("A Session activity request is required", nil)
	}
	q, binding, err := s.prepare(ctx, catalogInput{kind: "sessions", message: request, scope: request.Scope, page: request.Page, agent: request.Agent, query: request.Query})
	if err != nil {
		return nil, err
	}
	page, err := s.store.ListCatalogSessionActivity(ctx, q)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to list Session activity", err)
	}
	metadata, total, err := pageMetadata(binding, page.NextID, page.NextTime, page.Total)
	if err != nil {
		return nil, err
	}
	return &apiv1.ListSessionActivityResponse{Sessions: page.Sessions, TotalSize: total, Page: metadata}, nil
}

func (s *Service) ListAgentActivity(ctx context.Context, request *apiv1.ListAgentActivityRequest) (*apiv1.ListAgentActivityResponse, error) {
	if request == nil {
		return nil, serviceerrors.NewInvalidArgument("An Agent activity request is required", nil)
	}
	q, _, err := s.prepare(ctx, catalogInput{kind: "agents", message: request, scope: request.Scope})
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListCatalogAgentActivity(ctx, q.Scope)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to list Agent activity", err)
	}
	return &apiv1.ListAgentActivityResponse{Agents: rows}, nil
}

func pageMetadata(binding, id string, at *time.Time, total int) (*apiv1.PageResponse, int32, error) {
	if total < 0 || total > math.MaxInt32 {
		return nil, 0, serviceerrors.NewResourceExhausted("Task catalog exceeds its supported total", nil)
	}
	page, err := nextCursor(binding, id, at)
	if err != nil {
		return nil, 0, serviceerrors.NewInternal("Failed to encode catalog page", err)
	}
	return page, int32(total), nil
}
