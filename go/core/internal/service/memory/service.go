package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
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
	runtimeauth.SessionStore
	StoreAgentMemories(context.Context, ...*database.Memory) error
	SearchAgentMemory(context.Context, string, pgvector.Vector, int) ([]database.AgentMemorySearchResult, error)
	ListAgentMemories(context.Context, string) ([]database.Memory, error)
	DeleteAgentMemory(context.Context, string) error
	DeleteMemoryEntry(context.Context, string, string) error
}

// AgentPermissions resolves the caller's actions on the Agent that owns a memory.
type AgentPermissions interface {
	CheckPermissions(context.Context, *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error)
}

type Input struct {
	AgentName string
	Content   string
	Vector    []float32
	Metadata  json.RawMessage
	TTLDays   int
}

type SearchRequest struct {
	AgentName string
	Vector    []float32
	Limit     int
	MinScore  float64
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

// WithAgentPermissions admits user calls through the owning Agent's resource permissions.
func WithAgentPermissions(permissions AgentPermissions) Option {
	return func(service *Service) {
		service.permissions = permissions
	}
}

// Service stores memory per Agent. Every caller authorized for the Agent shares it:
// Actors use their session's Agent, and users act through the Agent's permissions.
type Service struct {
	store       Store
	authorizer  auth.Authorizer
	permissions AgentPermissions
	now         func() time.Time
}

func NewService(store Store, authorizer auth.Authorizer, options ...Option) *Service {
	service := &Service{store: store, authorizer: authorizer, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Add(ctx context.Context, input Input) (string, error) {
	agentName, author, err := s.memoryScope(ctx, auth.VerbCreate, input.AgentName)
	if err != nil {
		return "", err
	}
	if err := validateVector(input.Vector); err != nil {
		return "", err
	}
	if s.store == nil {
		return "", serviceerrors.NewInternal("failed to save memory", fmt.Errorf("database client is not configured"))
	}
	memory, err := s.toMemory(input, agentName, author)
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

	agentName, author, err := s.memoryScope(ctx, auth.VerbCreate, inputs[0].AgentName)
	if err != nil {
		return 0, err
	}
	memories := make([]*database.Memory, 0, len(inputs))
	for _, input := range inputs {
		if err := validateVector(input.Vector); err != nil {
			return 0, err
		}
		memory, err := s.toMemory(input, agentName, author)
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
	agentName, _, err := s.memoryScope(ctx, auth.VerbGet, request.AgentName)
	if err != nil {
		return nil, err
	}
	if err := validateVector(request.Vector); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, serviceerrors.NewInternal("search failed", fmt.Errorf("database client is not configured"))
	}
	if request.Limit <= 0 {
		request.Limit = DefaultSearchLimit
	}
	results, err := s.store.SearchAgentMemory(ctx, agentName, pgvector.NewVector(request.Vector), request.Limit)
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

func (s *Service) List(ctx context.Context, agentName string) ([]database.Memory, error) {
	agentName, _, err := s.memoryScope(ctx, auth.VerbList, agentName)
	if err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, serviceerrors.NewInternal("failed to list memories", fmt.Errorf("database client is not configured"))
	}
	memories, err := s.store.ListAgentMemories(ctx, agentName)
	if err != nil {
		return nil, serviceerrors.NewInternal("failed to list memories", err)
	}
	return memories, nil
}

func (s *Service) Delete(ctx context.Context, agentName string) error {
	agentName, _, err := s.memoryScope(ctx, auth.VerbDelete, agentName)
	if err != nil {
		return err
	}
	if s.store == nil {
		return serviceerrors.NewInternal("failed to delete memory", fmt.Errorf("database client is not configured"))
	}
	if err := s.store.DeleteAgentMemory(ctx, agentName); err != nil {
		return serviceerrors.NewInternal("failed to delete memory", err)
	}
	return nil
}

// memoryScope resolves the Agent whose memory the caller uses and the identity recorded
// as the author of new entries. Users name the Agent and need its resource permissions,
// even after their sessions end; Actors stay bound to their current session's Agent.
func (s *Service) memoryScope(ctx context.Context, verb auth.Verb, agentName string) (string, string, error) {
	if session, ok := auth.AuthSessionFrom(ctx); ok && session != nil {
		if principal := session.Principal(); principal.RuntimeBinding == nil && principal.ID != "" {
			agent, err := agentReference(agentName)
			if err != nil {
				return "", "", err
			}
			if err := s.authorize(ctx, verb, agentName); err != nil {
				return "", "", err
			}
			if err := s.authorizeAgent(ctx, agent, agentPermission(verb)); err != nil {
				return "", "", err
			}
			return agentName, principal.ID, nil
		}
	}
	return s.scope(ctx, verb)
}

// agentReference parses the "namespace/name" Agent key that user calls supply.
func agentReference(agentName string) (*apiv1.ResourceReference, error) {
	namespace, name, ok := strings.Cut(agentName, "/")
	if !ok || namespace == "" || name == "" || strings.Contains(name, "/") {
		return nil, serviceerrors.NewInvalidArgument("agent name must be namespace/name", nil)
	}
	return &apiv1.ResourceReference{Namespace: namespace, Name: name}, nil
}

// agentPermission maps a memory verb to the Agent action that admits it: reading shared
// memory follows read access, and changing it follows update access.
func agentPermission(verb auth.Verb) apiv1.Permission {
	if verb == auth.VerbGet || verb == auth.VerbList {
		return apiv1.Permission_PERMISSION_AGENT_READ
	}
	return apiv1.Permission_PERMISSION_AGENT_UPDATE
}

func (s *Service) authorizeAgent(ctx context.Context, agent *apiv1.ResourceReference, permission apiv1.Permission) error {
	if s.permissions == nil {
		return serviceerrors.NewUnavailable("Agent authorization is not configured", nil)
	}
	resource := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{Kubernetes: &apiv1.KubernetesAuthorizationReference{
		Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT, Ref: agent,
	}}}
	response, err := s.permissions.CheckPermissions(ctx, &apiv1.CheckPermissionsRequest{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: agent.Namespace},
		Checks:    []*apiv1.ResourceGrant{{Resource: resource, Permissions: []apiv1.Permission{permission}}},
	})
	if err != nil {
		return err
	}
	if access := response.GetResources(); len(access) != 1 || !slices.Contains(access[0].GetAllowedActions(), permission) {
		return serviceerrors.NewPermissionDenied("Not authorized to access Agent memory", nil)
	}
	return nil
}

// scope binds an Actor to its persisted session's Agent and records the session
// owner as the author. Request fields and forwarded headers never select another Agent.
func (s *Service) scope(ctx context.Context, verb auth.Verb) (string, string, error) {
	if s.store == nil {
		return "", "", serviceerrors.NewInternal("memory store is not configured", nil)
	}
	session, err := runtimeauth.Session(ctx, s.store)
	if err != nil {
		return "", "", err
	}
	if session.GetAgent().GetRef().GetNamespace() == "" || session.GetAgent().GetRef().GetName() == "" || session.Creator == "" {
		return "", "", serviceerrors.NewPermissionDenied("actor has no memory scope", nil)
	}
	agentName := session.Agent.Ref.Namespace + "/" + session.Agent.Ref.Name
	return agentName, session.Creator, s.authorize(ctx, verb, agentName)
}

func (s *Service) authorize(ctx context.Context, verb auth.Verb, agentName string) error {
	// Share authority belongs only to Session reads, including for direct callers.
	if _, shared := auth.ShareContextFrom(ctx); shared {
		return serviceerrors.NewPermissionDenied("Session shares do not grant Memory access", nil)
	}
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return serviceerrors.NewUnauthenticated("Authenticated identity is required", nil)
	}
	if err := s.authorizer.Check(ctx, session.Principal(), verb, auth.Resource{Type: "Memory", Name: agentName}); err != nil {
		return serviceerrors.NewPermissionDenied("Not authorized to access memory", err)
	}
	return nil
}

func (s *Service) toMemory(input Input, agentName, author string) (*database.Memory, error) {
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
		AgentName: agentName,
		UserID:    author,
		Content:   input.Content,
		Embedding: pgvector.NewVector(input.Vector),
		Metadata:  string(metadata),
		ExpiresAt: &expiresAt,
	}, nil
}

func validateVector(vector []float32) error {
	if len(vector) != VectorDimension {
		return serviceerrors.NewInvalidArgument(
			fmt.Sprintf("vector must have exactly %d dimensions, got %d", VectorDimension, len(vector)),
			nil,
		)
	}
	return nil
}
