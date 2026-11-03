package a2agateway

import (
	"context"
	"slices"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (s *gatewayTestStore) GetSessionByRequestID(_ context.Context, creator, requestID string) (*apiv1.Session, error) {
	if existing := s.created[creator+"/"+requestID]; existing != nil {
		return existing, nil
	}
	return nil, database.ErrNotFound
}
func (*gatewayTestStore) GetAgentRuntimeRevision(context.Context, string, string) (*database.RuntimeRevision, error) {
	return &database.RuntimeRevision{Revision: "authorized", Dependencies: &apiv1.ExecutionDependencyManifest{Direct: &apiv1.ResourceGrantSet{}}}, nil
}

// A2A adapter fixtures use deterministic application authority; native service
// integration tests cover live identity and OpenFGA decisions.
type gatewayCreationAuthority struct{ store *gatewayTestStore }

func (gatewayCreationAuthority) Bind(_ context.Context, id, creator string, _ *apiv1.ScopeCeiling) (*apiv1.SessionAuthority, error) {
	return &apiv1.SessionAuthority{SessionId: id, InitiatorId: creator, RootSessionId: id, WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{}}, nil
}
func (a gatewayCreationAuthority) Resolve(_ context.Context, id string) (auth.Session, error) {
	for _, session := range a.store.created {
		if session.Id == id {
			return auth.NewCredentialSession(auth.Principal{ID: session.Creator, Execution: session.Authority}, nil), nil
		}
	}
	if a.store.session != nil && a.store.session.Id == id {
		return auth.NewCredentialSession(auth.Principal{ID: a.store.session.Creator, Execution: a.store.session.Authority}, nil), nil
	}
	return nil, database.ErrNotFound
}
func (gatewayCreationAuthority) Renew(context.Context) error                   { return nil }
func (gatewayCreationAuthority) RevokeAuthority(context.Context, string) error { return nil }

type gatewayCreationPermissions struct{}

func (gatewayCreationPermissions) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	response := &apiv1.CheckPermissionsResponse{}
	for _, grant := range request.Checks {
		response.Resources = append(response.Resources, &apiv1.ResourceAccess{Resource: proto.CloneOf(grant.Resource), AllowedActions: grant.Permissions})
	}
	return response, nil
}
func (p gatewayCreationPermissions) CheckExecutionPermissions(ctx context.Context, principal auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	response, err := p.CheckPermissions(ctx, request)
	for _, access := range response.Resources {
		access.AllowedActions = slices.DeleteFunc(slices.Clone(access.AllowedActions), func(action apiv1.Permission) bool {
			return !auth.ResourceGrantAllows(principal.Execution.GetScopeCeiling().GetResources(), access.Resource, action)
		})
	}
	return response, err
}

func (a gatewayCreationAuthority) Session(ctx context.Context, id string) (*apiv1.Session, error) {
	return a.store.GetSessionByID(ctx, id)
}
