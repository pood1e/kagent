package authorization

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

const (
	testStoreID   = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testModelID   = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	testSessionID = "01990000-0000-7000-8000-000000000001"
	testIssuer    = "https://identity.example"
)

type testStore struct {
	mu          sync.Mutex
	sessions    map[string]*apiv1.Session
	tasks       map[string]string
	changes     map[string]database.AuthorizationChange
	applied     map[string]bool
	beginErr    error
	completeErr error
}

func (s *testStore) GetSessionByID(_ context.Context, id string) (*apiv1.Session, error) {
	session := s.sessions[id]
	if session == nil {
		return nil, database.ErrNotFound
	}
	return proto.CloneOf(session), nil
}

func (s *testStore) SessionForTask(_ context.Context, id string) (string, error) {
	if session, ok := s.tasks[id]; ok {
		return session, nil
	}
	return "", database.ErrNotFound
}

func (s *testStore) BeginAuthorizationChange(_ context.Context, change database.AuthorizationChange) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beginErr != nil {
		return false, s.beginErr
	}
	if s.changes == nil {
		s.changes = make(map[string]database.AuthorizationChange)
	}
	if prior, ok := s.changes[change.RequestID]; ok {
		if prior.ActorID != change.ActorID || prior.StoreID != change.StoreID || prior.ModelID != change.ModelID ||
			prior.Grant != change.Grant || !proto.Equal(prior.Binding, change.Binding) {
			return false, database.ErrIdempotencyConflict
		}
	} else {
		change.Binding = proto.CloneOf(change.Binding)
		s.changes[change.RequestID] = change
	}
	return s.applied[change.RequestID], nil
}

func (s *testStore) CompleteAuthorizationChange(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeErr != nil {
		return s.completeErr
	}
	if s.applied == nil {
		s.applied = make(map[string]bool)
	}
	s.applied[id] = true
	return nil
}

func testResources(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "workspace-1"}},
		&v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "assistant", UID: "agent-1"}},
		&v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "runtime", UID: "harness-1"}},
		&v1alpha3.ModelConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "primary", UID: "model-1"}},
		&v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "tools", UID: "mcp-1"}},
	).Build()
}

func workspaceReference() *apiv1.AuthorizationResource {
	return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"},
	}}
}

func agentReference() *apiv1.AuthorizationResource {
	return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
		Kubernetes: &apiv1.KubernetesAuthorizationReference{
			Kind: agentScope, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "assistant"},
		},
	}}
}

func userContext(t *testing.T, id string) context.Context {
	t.Helper()
	return auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(auth.Principal{ID: id}, nil))
}
