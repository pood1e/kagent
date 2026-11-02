package authorization

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	fgaclient "github.com/openfga/go-sdk/client"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

//go:embed model.json
var modelJSON []byte

type Store interface {
	resourceStore
	BeginAuthorizationChange(context.Context, database.AuthorizationChange) (bool, error)
	CompleteAuthorizationChange(context.Context, string) error
}

// Config binds trusted identity and resource sources to one immutable model.
type Config struct {
	OpenFGA    *configv1.OpenFGAConfig
	Kubernetes client.Reader
	Store      Store
	Issuer     string
}

type Service struct {
	client    *fgaclient.OpenFgaClient
	resolver  resolver
	store     Store
	config    *configv1.OpenFGAConfig
	issuer    string
	transport *http.Transport
}

func NewService(config Config) (*Service, error) {
	if config.Kubernetes == nil || config.Store == nil {
		return nil, fmt.Errorf("authorization requires resource and audit stores")
	}
	sdk, transport, err := newClient(config.OpenFGA)
	if err != nil {
		return nil, err
	}
	return &Service{
		client: sdk, transport: transport,
		resolver: resolver{kube: config.Kubernetes, store: config.Store},
		store:    config.Store, config: config.OpenFGA, issuer: config.Issuer,
	}, nil
}

// Close releases idle SDK connections; callers own the service lifetime.
func (s *Service) Close() { s.transport.CloseIdleConnections() }

// Ready verifies that the pinned model matches the contract shipped by this binary.
func (s *Service) Ready(ctx context.Context) error {
	model, err := s.client.ReadAuthorizationModel(ctx).Execute()
	if err != nil {
		return serviceerrors.NewUnavailable("Failed to read authorization model", err)
	}
	if model == nil || model.AuthorizationModel == nil {
		return serviceerrors.NewUnavailable("Incomplete authorization model response", nil)
	}
	expected := new(openfgav1.AuthorizationModel)
	if err := protojson.Unmarshal(modelJSON, expected); err != nil {
		return fmt.Errorf("decode embedded authorization model: %w", err)
	}
	body, err := json.Marshal(model.AuthorizationModel)
	if err != nil {
		return fmt.Errorf("encode configured authorization model: %w", err)
	}
	actual := new(openfgav1.AuthorizationModel)
	if err := protojson.Unmarshal(body, actual); err != nil {
		return fmt.Errorf("decode configured authorization model: %w", err)
	}
	actual.Id = ""
	if !proto.Equal(actual, expected) {
		return serviceerrors.NewFailedPrecondition("Configured authorization model differs from the native contract", nil)
	}
	return nil
}

func (s *Service) ListAccessRoles(ctx context.Context) (*apiv1.ListAccessRolesResponse, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	return &apiv1.ListAccessRolesResponse{Roles: accessRoles()}, nil
}

func caller(ctx context.Context) (auth.Principal, error) {
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session == nil || session.Principal().ID == "" {
		return auth.Principal{}, serviceerrors.NewUnauthenticated("Authenticated principal required", nil)
	}
	principal := session.Principal()
	if _, shared := auth.ShareContextFrom(ctx); shared || principal.RuntimeBinding != nil || principal.Execution != nil {
		return auth.Principal{}, serviceerrors.NewPermissionDenied("Authorization management requires an unbound identity", nil)
	}
	return principal, nil
}
