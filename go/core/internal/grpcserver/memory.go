package grpcserver

import (
	"context"
	"encoding/json"

	"google.golang.org/protobuf/types/known/timestamppb"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	memoryservice "github.com/kagent-dev/kagent/go/core/internal/service/memory"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

type memoryServer struct {
	runtimev1.UnimplementedMemoryServiceServer
	service *memoryservice.Service
}

func newMemoryServer(service *memoryservice.Service) *memoryServer {
	return &memoryServer{service: service}
}

func (s *memoryServer) AddSession(ctx context.Context, request *runtimev1.MemoryServiceAddSessionRequest) (*runtimev1.MemoryServiceAddSessionResponse, error) {
	input, err := memoryInputFromProto(request.GetMemory())
	if err != nil {
		return nil, err
	}
	input.Scope = request.GetScope()
	id, err := s.service.Add(ctx, input)
	if err != nil {
		return nil, err
	}
	return &runtimev1.MemoryServiceAddSessionResponse{Id: id}, nil
}

func (s *memoryServer) AddSessionBatch(ctx context.Context, request *runtimev1.MemoryServiceAddSessionBatchRequest) (*runtimev1.MemoryServiceAddSessionBatchResponse, error) {
	inputs := make([]memoryservice.Input, 0, len(request.GetItems()))
	for _, item := range request.GetItems() {
		input, err := memoryInputFromProto(item)
		if err != nil {
			return nil, err
		}
		input.Scope = request.GetScope()
		inputs = append(inputs, input)
	}
	count, err := s.service.AddBatch(ctx, inputs)
	if err != nil {
		return nil, err
	}
	return &runtimev1.MemoryServiceAddSessionBatchResponse{Count: int32(count)}, nil
}

func (s *memoryServer) Search(ctx context.Context, request *runtimev1.MemoryServiceSearchRequest) (*runtimev1.MemoryServiceSearchResponse, error) {
	return s.service.SearchProto(ctx, request)
}

func (s *memoryServer) List(ctx context.Context, request *runtimev1.MemoryServiceListRequest) (*runtimev1.MemoryServiceListResponse, error) {
	results, err := s.service.List(ctx, request.GetScope())
	if err != nil {
		return nil, err
	}

	memories := make([]*runtimev1.MemorySummary, 0, len(results))
	for index := range results {
		memories = append(memories, memorySummaryToProto(&results[index]))
	}
	return &runtimev1.MemoryServiceListResponse{Memories: memories}, nil
}

func (s *memoryServer) Delete(ctx context.Context, request *runtimev1.MemoryServiceDeleteRequest) (*runtimev1.MemoryServiceDeleteResponse, error) {
	if err := s.service.Delete(ctx, request.GetScope()); err != nil {
		return nil, err
	}
	return &runtimev1.MemoryServiceDeleteResponse{Status: "deleted"}, nil
}

func memoryInputFromProto(input *runtimev1.SessionMemoryInput) (memoryservice.Input, error) {
	if input == nil {
		return memoryservice.Input{}, nil
	}
	var metadata json.RawMessage
	if input.GetMetadata() != nil {
		encoded, err := json.Marshal(input.GetMetadata().AsMap())
		if err != nil {
			return memoryservice.Input{}, serviceerrors.NewInvalidArgument("metadata is invalid", err)
		}
		metadata = encoded
	}
	return memoryservice.Input{
		Content:  input.GetContent(),
		Vector:   input.GetVector(),
		Metadata: metadata,
		TTLDays:  int(input.GetTtlDays()),
	}, nil
}

func memorySummaryToProto(memory *database.Memory) *runtimev1.MemorySummary {
	result := &runtimev1.MemorySummary{
		Id:          memory.ID,
		Content:     memory.Content,
		AccessCount: memory.AccessCount,
	}
	if !memory.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(memory.CreatedAt)
	}
	if memory.ExpiresAt != nil {
		result.ExpiresAt = timestamppb.New(*memory.ExpiresAt)
	}
	return result
}
