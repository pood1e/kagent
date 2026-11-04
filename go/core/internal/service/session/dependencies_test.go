package session

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"buf.build/go/protovalidate"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type creationAuthority struct{}

func (creationAuthority) Bind(_ context.Context, id, creator string, _ *apiv1.ScopeCeiling) (*apiv1.SessionAuthority, error) {
	return &apiv1.SessionAuthority{SessionId: id, InitiatorId: creator, RootSessionId: id, WorkspaceUid: "workspace-1", ScopeCeiling: &apiv1.ScopeCeiling{}}, nil
}
func (creationAuthority) Resolve(context.Context, string) (auth.Session, error) {
	panic("unexpected resolution")
}
func (creationAuthority) Renew(context.Context) error                   { return nil }
func (creationAuthority) RevokeAuthority(context.Context, string) error { return nil }

type creationPermissions struct{ err error }

func (p creationPermissions) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	response := &apiv1.CheckPermissionsResponse{}
	for _, grant := range request.Checks {
		response.Resources = append(response.Resources, &apiv1.ResourceAccess{Resource: proto.CloneOf(grant.Resource), AllowedActions: grant.Permissions})
	}
	return response, nil
}
func (p creationPermissions) CheckExecutionPermissions(ctx context.Context, principal auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	response, err := p.CheckPermissions(ctx, request)
	if err != nil {
		return nil, err
	}
	for _, access := range response.Resources {
		access.AllowedActions = slices.DeleteFunc(slices.Clone(access.AllowedActions), func(action apiv1.Permission) bool {
			return !auth.ResourceGrantAllows(principal.Execution.GetScopeCeiling().GetResources(), access.Resource, action)
		})
	}
	return response, nil
}
func (s *serviceTestStore) GetAgentRuntimeRevision(context.Context, string, string) (*database.RuntimeRevision, error) {
	s.revisionCalls++
	if s.revisionErr != nil {
		return nil, s.revisionErr
	}
	return &database.RuntimeRevision{Revision: "authorized-revision", Dependencies: &apiv1.ExecutionDependencyManifest{Direct: &apiv1.ResourceGrantSet{}}}, nil
}
func (s *serviceTestStore) GetRuntimeRevision(context.Context, string) (*database.RuntimeRevision, error) {
	if s.revisionErr != nil {
		return nil, s.revisionErr
	}
	if s.revision != nil {
		return s.revision, nil
	}
	return &database.RuntimeRevision{Namespace: "team-a", Dependencies: &apiv1.ExecutionDependencyManifest{Direct: &apiv1.ResourceGrantSet{}}}, nil
}
func (s *serviceTestStore) GetSessionByRequestID(context.Context, string, string) (*apiv1.Session, error) {
	if s.existing != nil {
		return s.existing, nil
	}
	return nil, database.ErrNotFound
}
func createTestService(store *serviceTestStore, authorizer auth.Authorizer, workflow sessionWorkflow) *Service {
	return NewService(store, authorizer, workflow, WithExecutionAuthority(creationAuthority{}), WithResourceAuthorization(creationPermissions{}))
}

func TestSessionCreationFailsClosedWithoutResourceAuthorization(t *testing.T) {
	store := &serviceTestStore{}
	service := NewService(store, serviceTestAuthorizer{}, serviceTestWorkflow{}, WithExecutionAuthority(creationAuthority{}))
	_, err := service.Create(serviceTestContext("alice"), &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}, "request-1", "", nil)
	require.Error(t, err)
	require.Nil(t, store.createInput)
}

func TestSessionReservationPinsTheAuthorizedRevisionAndCeiling(t *testing.T) {
	store := &serviceTestStore{}
	service := createTestService(store, serviceTestAuthorizer{}, serviceTestWorkflow{})
	result, err := service.Create(serviceTestContext("alice"), &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}, "request-1", "", nil)
	require.NoError(t, err)
	require.Equal(t, "authorized-revision", store.createInput.PreparedRevision)
	require.NotNil(t, result.Authority.ScopeCeiling.Resources)
	require.Len(t, result.Authority.ScopeCeiling.Resources.Grants, 1)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_SESSION_CREATE}, result.Authority.ScopeCeiling.Resources.Grants[0].Permissions)
}

func TestCreationRetryReauthorizesItsOriginalCeilingWithoutReadingNewTargets(t *testing.T) {
	store := &serviceTestStore{}
	service := createTestService(store, serviceTestAuthorizer{}, serviceTestWorkflow{})
	agent := &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}
	original, err := service.Create(serviceTestContext("alice"), agent, "request-1", "original", nil)
	require.NoError(t, err)
	store.existing = original
	store.revisionErr = database.ErrNotFound
	retried, err := service.Create(serviceTestContext("alice"), agent, "request-1", "renamed", nil)
	require.NoError(t, err)
	require.Equal(t, original.Id, retried.Id)
	require.Equal(t, 1, store.revisionCalls)
	_, err = service.Create(serviceTestContext("alice"), agent, "request-1", "", &apiv1.ScopeCeiling{Resources: &apiv1.ResourceGrantSet{}})
	require.Error(t, err, "an idempotency key cannot bypass an explicitly narrowed ceiling")
}

func TestDependencyChecksStayWithinTheAPILimit(t *testing.T) {
	server := &apiv1.KubernetesAuthorizationReference{
		Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER,
		Ref:  &apiv1.ResourceReference{Namespace: "team", Name: "tools"}, Uid: "server-1",
	}
	dependencies := &apiv1.ResourceGrantSet{}
	for i := range 2*auth.MaxPermissionChecks + 1 {
		dependencies.Grants = append(dependencies.Grants, &apiv1.ResourceGrant{
			Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{
				McpTool: &apiv1.MCPToolAuthorizationReference{Server: server, Name: fmt.Sprintf("tool-%d", i)},
			}},
			Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL},
		})
	}
	var sizes []int
	check := func(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
		require.NoError(t, protovalidate.Validate(request))
		sizes = append(sizes, len(request.Checks))
		return creationPermissions{}.CheckPermissions(ctx, request)
	}
	workspace := &apiv1.WorkspaceAuthorizationReference{Namespace: "team", Uid: "workspace-1"}
	require.NoError(t, requireDependencies(t.Context(), check, workspace, dependencies))
	require.Equal(t, []int{auth.MaxPermissionChecks, auth.MaxPermissionChecks, 1}, sizes)
}
