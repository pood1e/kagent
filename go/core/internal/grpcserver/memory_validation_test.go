package grpcserver

import (
	"context"
	"math"
	"testing"

	"buf.build/go/protovalidate"
	middleware "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
)

func TestMemoryValidationBeforeHandler(t *testing.T) {
	validator, err := protovalidate.New()
	require.NoError(t, err)
	team := &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_TeamNamespace{TeamNamespace: "team-a"}}
	for _, test := range []struct {
		name    string
		request proto.Message
		valid   bool
	}{
		{"text add", &runtimev1.MemoryServiceAddSessionRequest{Scope: team, Memory: &runtimev1.SessionMemoryInput{Content: "finding"}}, true},
		{"empty content", &runtimev1.MemoryServiceAddSessionRequest{Scope: team, Memory: &runtimev1.SessionMemoryInput{}}, false},
		{"missing memory", &runtimev1.MemoryServiceAddSessionRequest{Scope: team}, false},
		{"empty batch", &runtimev1.MemoryServiceAddSessionBatchRequest{Scope: team}, false},
		{"text search", &runtimev1.MemoryServiceSearchRequest{Scope: team, Query: "finding"}, true},
		{"vector search", &runtimev1.MemoryServiceSearchRequest{Vector: memoryVector()}, true},
		{"both search inputs", &runtimev1.MemoryServiceSearchRequest{Query: "finding", Vector: memoryVector()}, false},
		{"no search input", &runtimev1.MemoryServiceSearchRequest{}, false},
		{"blank search", &runtimev1.MemoryServiceSearchRequest{Query: "  "}, false},
		{"short vector", &runtimev1.MemoryServiceSearchRequest{Vector: []float32{1}}, false},
		{"nonfinite vector", &runtimev1.MemoryServiceAddSessionRequest{Memory: &runtimev1.SessionMemoryInput{Content: "finding", Vector: []float32{float32(math.NaN())}}}, false},
		{"bad namespace", &runtimev1.MemoryServiceListRequest{Scope: &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_TeamNamespace{TeamNamespace: "Team A"}}}, false},
		{"empty scope", &runtimev1.MemoryServiceListRequest{Scope: &runtimev1.MemoryScope{}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			handled := false
			_, err := middleware.UnaryServerInterceptor(validator)(t.Context(), test.request, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
				handled = true
				return nil, nil
			})
			require.Equal(t, test.valid, handled)
			want := codes.InvalidArgument
			if test.valid {
				want = codes.OK
			}
			require.Equal(t, want, status.Code(err), "%v", err)
		})
	}
}
