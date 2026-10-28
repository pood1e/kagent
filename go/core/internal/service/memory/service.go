package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

const (
	VectorDimension    = 768
	MaxBatchSize       = 50
	DefaultTTLDays     = 15
	DefaultSearchLimit = 5
)

type Store interface {
	GetSessionForRuntime(context.Context, string, string) (*apiv1.Session, error)
	StoreAgentMemories(context.Context, ...*database.Memory) error
	SearchAgentMemory(context.Context, *runtimev1.MemoryScope, string, pgvector.Vector, int) ([]database.AgentMemorySearchResult, error)
	ListAgentMemories(context.Context, *runtimev1.MemoryScope, string) ([]database.Memory, error)
	DeleteAgentMemory(context.Context, *runtimev1.MemoryScope, string) error
	DeleteMemoryEntry(context.Context, *runtimev1.MemoryScope, string, string) error
}

type Input struct {
	Scope    *runtimev1.MemoryScope
	Content  string
	Vector   []float32
	Metadata json.RawMessage
	TTLDays  int
}

type SearchRequest struct {
	Query    string
	Scope    *runtimev1.MemoryScope
	Vector   []float32
	Limit    int
	MinScore float64
}

type SearchResult struct {
	ID        string
	Content   string
	Score     float64
	Metadata  json.RawMessage
	CreatedAt time.Time
}

type Option func(*Service)

func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		service.now = now
	}
}

type Service struct {
	store      Store
	authorizer auth.Authorizer
	now        func() time.Time
	embedder   Embedder
}

func NewService(store Store, authorizer auth.Authorizer, options ...Option) *Service {
	service := &Service{store: store, authorizer: authorizer, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Add(ctx context.Context, input Input) (string, error) {
	scope, userID, err := s.managementScope(ctx, auth.VerbCreate, input.Scope)
	if err != nil {
		return "", err
	}
	if input.Vector, err = s.vector(ctx, input.Vector, input.Content); err != nil {
		return "", err
	}
	if s.store == nil {
		return "", serviceerrors.NewInternal("failed to save memory", fmt.Errorf("database client is not configured"))
	}
	memory, err := s.toMemory(input, scope, userID)
	if err != nil {
		return "", err
	}
	if err := s.store.StoreAgentMemories(ctx, memory); err != nil {
		return "", serviceerrors.NewInternal("failed to save memory", err)
	}
	return memory.ID, nil
}

func (s *Service) AddBatch(ctx context.Context, inputs []Input) (int, error) {
	if len(inputs) == 0 {
		return 0, serviceerrors.NewInvalidArgument("Empty batch", nil)
	}
	if len(inputs) > MaxBatchSize {
		return 0, serviceerrors.NewInvalidArgument(
			fmt.Sprintf("batch size %d exceeds maximum allowed size of %d", len(inputs), MaxBatchSize),
			nil,
		)
	}
	if s.store == nil {
		return 0, serviceerrors.NewInternal("failed to save memory batch", fmt.Errorf("database client is not configured"))
	}

	scope, userID, err := s.managementScope(ctx, auth.VerbCreate, inputs[0].Scope)
	if err != nil {
		return 0, err
	}
	inputs, err = s.batchVectors(ctx, inputs)
	if err != nil {
		return 0, err
	}
	memories := make([]*database.Memory, 0, len(inputs))
	for _, input := range inputs {
		memory, err := s.toMemory(input, scope, userID)
		if err != nil {
			return 0, err
		}
		memories = append(memories, memory)
	}
	if err := s.store.StoreAgentMemories(ctx, memories...); err != nil {
		return 0, serviceerrors.NewInternal("failed to save memory batch", err)
	}
	return len(memories), nil
}

func (s *Service) Search(ctx context.Context, request SearchRequest) ([]SearchResult, error) {
	scope, userID, err := s.managementScope(ctx, auth.VerbGet, request.Scope)
	if err != nil {
		return nil, err
	}
	if len(request.Vector) > 0 && request.Query != "" {
		return nil, serviceerrors.NewInvalidArgument("supply query or vector", nil)
	}
	if request.Vector, err = s.vector(ctx, request.Vector, request.Query); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, serviceerrors.NewInternal("search failed", fmt.Errorf("database client is not configured"))
	}
	if request.Limit <= 0 {
		request.Limit = DefaultSearchLimit
	}
	results, err := s.store.SearchAgentMemory(
		ctx,
		scope,
		userID,
		pgvector.NewVector(request.Vector),
		request.Limit,
	)
	if err != nil {
		return nil, serviceerrors.NewInternal("search failed", err)
	}

	response := make([]SearchResult, 0, len(results))
	for _, result := range results {
		if request.MinScore > 0 && result.Score < request.MinScore {
			continue
		}
		metadata := json.RawMessage(result.Metadata)
		if len(metadata) == 0 || !json.Valid(metadata) {
			metadata = json.RawMessage("{}")
		}
		response = append(response, SearchResult{
			ID:        result.ID,
			Content:   result.Content,
			Score:     result.Score,
			Metadata:  metadata,
			CreatedAt: result.CreatedAt,
		})
	}
	return response, nil
}

func (s *Service) List(ctx context.Context, requested *runtimev1.MemoryScope) ([]database.Memory, error) {
	scope, userID, err := s.managementScope(ctx, auth.VerbList, requested)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		return nil, serviceerrors.NewInvalidArgument("Memory scope is required", nil)
	}
	if s.store == nil {
		return nil, serviceerrors.NewInternal("failed to list memories", fmt.Errorf("database client is not configured"))
	}
	memories, err := s.store.ListAgentMemories(ctx, scope, userID)
	if err != nil {
		return nil, serviceerrors.NewInternal("failed to list memories", err)
	}
	return memories, nil
}

func (s *Service) Delete(ctx context.Context, requested *runtimev1.MemoryScope) error {
	scope, userID, err := s.managementScope(ctx, auth.VerbDelete, requested)
	if err != nil {
		return err
	}
	if scope == nil {
		return serviceerrors.NewInvalidArgument("Memory scope is required", nil)
	}
	if s.store == nil {
		return serviceerrors.NewInternal("failed to delete memory", fmt.Errorf("database client is not configured"))
	}
	if err := s.store.DeleteAgentMemory(ctx, scope, userID); err != nil {
		return serviceerrors.NewInternal("failed to delete memory", err)
	}
	return nil
}

func (s *Service) toMemory(input Input, scope *runtimev1.MemoryScope, userID string) (*database.Memory, error) {
	metadata := input.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	} else if !json.Valid(metadata) {
		return nil, serviceerrors.NewInvalidArgument("metadata must be valid JSON", nil)
	}
	ttlDays := input.TTLDays
	if ttlDays <= 0 {
		ttlDays = DefaultTTLDays
	}
	expiresAt := s.now().Add(time.Duration(ttlDays) * 24 * time.Hour)
	return &database.Memory{
		AgentName:     scope.GetAgentName(),
		TeamNamespace: scope.GetTeamNamespace(),
		UserID:        userID,
		Content:       input.Content,
		Embedding:     pgvector.NewVector(input.Vector),
		Metadata:      string(metadata),
		ExpiresAt:     &expiresAt,
	}, nil
}

func validateVector(vector []float32) error {
	if !finiteVector(vector) {
		return serviceerrors.NewInvalidArgument("vector values must be finite", nil)
	}
	if len(vector) != VectorDimension {
		return serviceerrors.NewInvalidArgument(
			fmt.Sprintf("vector must have exactly %d dimensions, got %d", VectorDimension, len(vector)),
			nil,
		)
	}
	return nil
}
