package grpcserver

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	memorysvc "github.com/kagent-dev/kagent/go/core/internal/service/memory"
)

type rpcEmbedder struct{ calls atomic.Int32 }

func (e *rpcEmbedder) Generate(_ context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = memoryVector()
	}
	return result, nil
}

func TestTeamMemoryTextThroughGeneratedClient(t *testing.T) {
	store := &memoryRPCStore{rows: map[string][]database.Memory{}}
	policy := &memoryRPCPolicy{}
	embedder := &rpcEmbedder{}
	connection := newConfigurationConnection(t, nil, func(config *Config) {
		config.Authenticator = authimpl.NewProxyAuthenticator("sub")
		config.MemoryService = memorysvc.NewService(store, policy, memorysvc.WithEmbedder(embedder))
	})
	client := runtimev1.NewMemoryServiceClient(connection)
	scope := &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_TeamNamespace{TeamNamespace: "team-a"}}
	created, err := client.AddSession(memoryUserContext(t, "alice"), &runtimev1.MemoryServiceAddSessionRequest{Scope: scope, Memory: &runtimev1.SessionMemoryInput{Content: "shared finding"}})
	require.NoError(t, err)
	found, err := client.Search(memoryUserContext(t, "bob"), &runtimev1.MemoryServiceSearchRequest{Scope: scope, Query: "finding"})
	require.NoError(t, err)
	require.Len(t, found.Memories, 1)
	require.Equal(t, created.Id, found.Memories[0].Id)
	private, err := client.List(memoryUserContext(t, "bob"), &runtimev1.MemoryServiceListRequest{Scope: personalMemoryScope("team-a/assistant")})
	require.NoError(t, err)
	require.Empty(t, private.Memories)
	policy.denied.Store(true)
	_, err = client.Search(memoryUserContext(t, "bob"), &runtimev1.MemoryServiceSearchRequest{Scope: scope, Query: "denied"})
	assertGRPCCode(t, err, codes.PermissionDenied)
	require.Equal(t, int32(2), embedder.calls.Load())
	policy.denied.Store(false)
	_, err = client.DeleteEntry(memoryUserContext(t, "viewer"), &runtimev1.MemoryServiceDeleteEntryRequest{Scope: scope, Id: created.Id})
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = client.DeleteEntry(memoryUserContext(t, "alice"), &runtimev1.MemoryServiceDeleteEntryRequest{Scope: scope, Id: created.Id})
	require.NoError(t, err)
}
