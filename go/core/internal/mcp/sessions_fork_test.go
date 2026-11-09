package mcp

import (
	"context"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// nativeSessionService creates Sessions through native execution authority,
// which every Session reservation requires.
func nativeSessionService(store *sessionStore) *sessionsvc.Service {
	return sessionsvc.NewService(store, &auth.NoopAuthorizer{}, &fakeSessionWorkflow{},
		sessionsvc.WithExecutionAuthority(sessionAuthority{}), sessionsvc.WithResourceAuthorization(sessionPermissions{}))
}

// GetAgentRuntimeRevision answers a prepared revision without dependencies.
func (*sessionStore) GetAgentRuntimeRevision(context.Context, string, string) (*database.RuntimeRevision, error) {
	return &database.RuntimeRevision{Revision: "revision", Dependencies: &apiv1.ExecutionDependencyManifest{Direct: &apiv1.ResourceGrantSet{}}}, nil
}

type sessionAuthority struct{}

func (sessionAuthority) Bind(_ context.Context, id, creator string, _ *apiv1.ScopeCeiling) (*apiv1.SessionAuthority, error) {
	return &apiv1.SessionAuthority{SessionId: id, InitiatorId: creator, RootSessionId: id, WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{}}, nil
}
func (sessionAuthority) Resolve(context.Context, string) (auth.Session, error) {
	panic("unexpected resolution")
}
func (sessionAuthority) Renew(context.Context) error                   { return nil }
func (sessionAuthority) RevokeAuthority(context.Context, string) error { return nil }

type sessionPermissions struct{}

func (sessionPermissions) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	response := &apiv1.CheckPermissionsResponse{}
	for _, check := range request.Checks {
		response.Resources = append(response.Resources, &apiv1.ResourceAccess{Resource: proto.CloneOf(check.Resource), AllowedActions: check.Permissions})
	}
	return response, nil
}
func (p sessionPermissions) CheckExecutionPermissions(ctx context.Context, _ auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	return p.CheckPermissions(ctx, request)
}
