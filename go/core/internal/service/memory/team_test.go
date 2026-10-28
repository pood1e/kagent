package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type countingEmbedder struct {
	calls   int
	err     error
	vectors [][]float32
}

func (e *countingEmbedder) Generate(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	if e.vectors != nil {
		return e.vectors, e.err
	}
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = vector(VectorDimension, .5)
	}
	return result, e.err
}

type teamPolicy struct {
	auth.NoopAuthorizer
	denied   bool
	resource auth.Resource
}

func (p *teamPolicy) Check(_ context.Context, _ auth.Principal, _ auth.Verb, resource auth.Resource) error {
	p.resource = resource
	if p.denied {
		return errors.New("revoked")
	}
	return nil
}

func TestTeamMemoryAuthorizesBeforeEmbeddingAndStorage(t *testing.T) {
	store := &memoryStore{}
	embedder := &countingEmbedder{}
	policy := &teamPolicy{}
	service := NewService(store, policy, WithEmbedder(embedder))
	team := &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_TeamNamespace{TeamNamespace: "team-a"}}
	ctx := auth.AuthSessionTo(t.Context(), &authimpl.SimpleSession{P: auth.Principal{ID: "alice"}})
	_, err := service.Add(ctx, Input{Scope: team, Content: "verified finding"})
	require.NoError(t, err)
	require.Equal(t, auth.Resource{Type: "TeamMemory", Namespace: "team-a"}, policy.resource)
	require.Len(t, store.stored, 1)
	require.Equal(t, "team-a", store.stored[0].TeamNamespace)
	require.Empty(t, store.stored[0].AgentName)
	require.Empty(t, store.stored[0].UserID)
	_, err = service.Add(memoryContext(t), Input{Scope: team, Content: "runtime finding"})
	require.NoError(t, err)
	calls := embedder.calls
	policy.denied = true
	_, err = service.Add(ctx, Input{Scope: team, Content: "denied"})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	require.Equal(t, calls, embedder.calls)
	policy.denied = false
	foreign := &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_TeamNamespace{TeamNamespace: "team-b"}}
	_, err = service.Add(memoryContext(t), Input{Scope: foreign, Content: "denied"})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	require.Equal(t, calls, embedder.calls)
	embedder.err = errors.New("provider failed")
	_, err = service.Add(ctx, Input{Scope: team, Content: "retry"})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
	require.Len(t, store.stored, 2)
	embedder.err = nil
	embedder.vectors = [][]float32{{1}}
	_, err = service.Search(ctx, SearchRequest{Scope: team, Query: "finding"})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInternal))
}

func TestBatchEmbeddingIsOneAuthorizedAtomicRequest(t *testing.T) {
	store := &memoryStore{}
	embedder := &countingEmbedder{}
	service := NewService(store, auth.NoopAuthorizer{}, WithEmbedder(embedder))
	inputs := []Input{{Content: "first"}, {Content: "second"}}
	count, err := service.AddBatch(memoryContext(t), inputs)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, 1, embedder.calls)
	require.Len(t, store.stored, 2)
	require.Nil(t, inputs[0].Vector, "input remains caller-owned")
	embedder.vectors = [][]float32{vector(VectorDimension, 1)}
	_, err = service.AddBatch(memoryContext(t), inputs)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInternal))
	require.Len(t, store.stored, 2, "malformed provider response never persists a partial batch")
}
