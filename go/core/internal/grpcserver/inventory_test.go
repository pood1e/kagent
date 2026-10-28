package grpcserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/inventory"
	"github.com/kagent-dev/kagent/go/core/internal/substrate"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type inventoryRPCStore struct {
	memoryRPCStore
	catalogs map[string]*apiv1.BuiltinToolCatalog
}

func (s *inventoryRPCStore) SaveToolInventory(_ context.Context, catalog *apiv1.BuiltinToolCatalog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.catalogs[catalog.Revision] = proto.Clone(catalog).(*apiv1.BuiltinToolCatalog)
	return nil
}

func (s *inventoryRPCStore) GetToolInventory(_ context.Context, revision string) (*apiv1.BuiltinToolCatalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if catalog := s.catalogs[revision]; catalog != nil {
		return proto.Clone(catalog).(*apiv1.BuiltinToolCatalog), nil
	}
	return nil, database.ErrNotFound
}

func TestToolInventoryGeneratedClientBindsVerifiedRevision(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	store := &inventoryRPCStore{memoryRPCStore: memoryRPCStore{session: &apiv1.Session{Id: id, Creator: "owner", PreparedRevision: "revision", Agent: &apiv1.ResourceReference{Namespace: "team", Name: "assistant"}, A2AAuthority: substrate.ActorHost("team", substrate.ActorName(id), "")}}, catalogs: make(map[string]*apiv1.BuiltinToolCatalog)}
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	agent := &v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "assistant"}, Status: v1alpha3.AgentStatus{LatestSuccessfulRevision: "revision"}}
	other := agent.DeepCopy()
	other.Name, other.Status.LatestSuccessfulRevision = "other", "unobserved"
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, other).Build()
	issuer := jwttest.New(t)
	verifier, err := substrate.NewActorJWTVerifier(t.Context(), issuer.URL, issuer.CAFile)
	require.NoError(t, err)
	connection := newConfigurationConnection(t, kube, func(config *Config) {
		config.Authenticator = runtimeauth.NewProvider(authimpl.NewProxyAuthenticator("sub"), verifier, store, issuer.URL, nil)
		config.InventoryService = inventory.NewService(store, runtimeauth.NewAuthorizer(&auth.NoopAuthorizer{}, store, nil))
	})
	client := runtimev1.NewToolInventoryServiceClient(connection)
	agents := apiv1.NewAgentServiceClient(connection)
	actor := func(uid string) context.Context {
		token := issuer.Token(t, issuer.Claims("team", substrate.ActorName(id), uid))
		return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token, "x-user-id", "forged"))
	}
	read := func(name string) *apiv1.BuiltinToolCatalog {
		t.Helper()
		response, err := agents.GetAgent(memoryUserContext(t, "owner"), &apiv1.GetAgentRequest{Ref: &apiv1.ResourceReference{Namespace: "team", Name: name}})
		require.NoError(t, err)
		return response.Agent.BuiltinTools
	}
	require.Nil(t, read("assistant"))
	request := &runtimev1.ToolInventoryServiceReportRequest{Tools: []*apiv1.DiscoveredTool{{Name: "read_file"}, {Name: "bash", Description: "Execute a command"}}}
	_, err = client.Report(memoryUserContext(t, "owner"), request)
	assertGRPCCode(t, err, codes.PermissionDenied)
	_, err = client.Report(actor("stale"), request)
	assertGRPCCode(t, err, codes.Unauthenticated)
	_, err = client.Report(actor("actor-uid"), &runtimev1.ToolInventoryServiceReportRequest{Tools: []*apiv1.DiscoveredTool{{Name: "duplicate"}, {Name: "duplicate"}}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	require.Zero(t, store.callCount(), "invalid or unauthenticated observations must not reach persistence")
	_, err = client.Report(actor("actor-uid"), request)
	require.NoError(t, err)
	catalog := read("assistant")
	require.Equal(t, "revision", catalog.Revision)
	require.NoError(t, catalog.ObservedAt.CheckValid())
	require.Equal(t, "bash", catalog.Tools[0].Name)
	require.Nil(t, read("other"), "observations cannot spill to another revision")
	_, err = client.Report(actor("actor-uid"), &runtimev1.ToolInventoryServiceReportRequest{})
	require.NoError(t, err)
	require.NotNil(t, read("assistant"))
	require.Empty(t, read("assistant").Tools, "an observed empty catalog differs from an unknown catalog")
}
