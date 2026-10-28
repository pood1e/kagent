package memory

import (
	"context"
	"encoding/json"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

// SearchProto applies the same scope and embedding contract for gRPC and MCP.
func (s *Service) SearchProto(ctx context.Context, request *runtimev1.MemoryServiceSearchRequest) (*runtimev1.MemoryServiceSearchResponse, error) {
	results, err := s.Search(ctx, SearchRequest{Scope: request.GetScope(), Query: request.GetQuery(), Vector: request.GetVector(), Limit: int(request.GetLimit()), MinScore: request.GetMinScore()})
	if err != nil {
		return nil, err
	}
	output := &runtimev1.MemoryServiceSearchResponse{}
	for _, result := range results {
		var values map[string]any
		if err := json.Unmarshal(result.Metadata, &values); err != nil {
			return nil, serviceerrors.NewInternal("Failed to decode memory metadata", err)
		}
		metadata, err := structpb.NewStruct(values)
		if err != nil {
			return nil, serviceerrors.NewInternal("Failed to encode memory metadata", err)
		}
		item := &runtimev1.MemorySearchResult{Id: result.ID, Content: result.Content, Score: result.Score, Metadata: metadata}
		if !result.CreatedAt.IsZero() {
			item.CreatedAt = timestamppb.New(result.CreatedAt)
		}
		output.Memories = append(output.Memories, item)
	}
	return output, nil
}
