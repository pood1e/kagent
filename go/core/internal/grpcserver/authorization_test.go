package grpcserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	fga "github.com/openfga/go-sdk"
	fgaclient "github.com/openfga/go-sdk/client"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/authorization"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/dbtest"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/kagent-dev/kagent/go/core/pkg/env"
)

func TestNativeOpenFGAAuthorizationAPI(t *testing.T) {
	endpoint := env.TestOpenFGAURL.Get()
	if endpoint == "" {
		t.Skip("set KAGENT_TEST_OPENFGA_URL to run real OpenFGA API integration")
	}
	ctx := t.Context()
	fgaClient, err := fgaclient.NewSdkClient(&fgaclient.ClientConfiguration{ApiUrl: endpoint})
	require.NoError(t, err)
	store, err := fgaClient.CreateStore(ctx).Body(fgaclient.ClientCreateStoreRequest{Name: "native-authorization-" + uuid.NewString()}).Execute()
	require.NoError(t, err)
	require.NoError(t, fgaClient.SetStoreId(store.Id))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := fgaClient.DeleteStore(cleanup).Execute()
		require.NoError(t, err)
	})
	modelJSON, err := os.ReadFile("../authorization/model.json")
	require.NoError(t, err)
	var model fga.WriteAuthorizationModelRequest
	require.NoError(t, json.Unmarshal(modelJSON, &model))
	written, err := fgaClient.WriteAuthorizationModel(ctx).Body(model).Execute()
	require.NoError(t, err)
	require.NoError(t, fgaClient.SetAuthorizationModelId(written.AuthorizationModelId))
	issuer := jwttest.New(t)
	provider, err := authimpl.NewOIDCAuthenticator(ctx, &configv1.OIDCAuthentication{
		Issuer: issuer.URL, Audience: "kagent-api", CaFile: issuer.CAFile,
	})
	require.NoError(t, err)
	subject := func(id string) *apiv1.SubjectReference {
		return &apiv1.SubjectReference{Subject: &apiv1.SubjectReference_PrincipalId{
			PrincipalId: auth.PrincipalID("oidc:"+issuer.URL, id),
		}}
	}
	adminID := subject("admin").GetPrincipalId()
	adminKey := "principal:" + base64.RawURLEncoding.EncodeToString([]byte(adminID))
	_, err = fgaClient.Write(ctx).Body(fgaclient.ClientWriteRequest{Writes: []fga.TupleKey{
		{User: adminKey, Relation: "member", Object: "workspace:workspace-1"},
		{User: adminKey, Relation: "access_admin", Object: "workspace:workspace-1"},
	}}).Execute()
	require.NoError(t, err)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "workspace-1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-team", UID: "workspace-2"}},
		&v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Name: "assistant", Namespace: "team", UID: "agent-1"}},
	).Build()
	connStr := dbtest.StartT(ctx, t)
	dbtest.MigrateT(t, connStr, false)
	pool, err := database.Connect(ctx, &database.PostgresConfig{Pool: dbtest.PoolConfig(connStr), Schema: "public", VectorSchema: "public"})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	service, err := authorization.NewService(authorization.Config{
		OpenFGA:    &configv1.OpenFGAConfig{ApiUrl: endpoint, StoreId: store.Id, AuthorizationModelId: written.AuthorizationModelId, GroupClaim: "groups"},
		Kubernetes: kube, Store: database.NewClient(pool, "public"), Issuer: issuer.URL,
	})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	require.NoError(t, service.Ready(ctx))
	connection := newConfigurationConnection(t, kube, func(config *Config) {
		config.Authenticator = provider
		config.AuthorizationService = service
	})
	client := apiv1.NewAuthorizationServiceClient(connection)
	identity := func(id string, groups []string) context.Context {
		token := issuer.Token(t, jwt.MapClaims{
			"iss": issuer.URL, "sub": id, "aud": "kagent-api",
			"exp": time.Now().Add(5 * time.Minute).Unix(), "groups": groups,
		})
		return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token, "x-user-id", "forged-admin"))
	}
	admin, alice := identity("admin", []string{}), identity("alice", []string{})
	workspace := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"},
	}}
	agent := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
		Kubernetes: &apiv1.KubernetesAuthorizationReference{
			Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT,
			Ref:  &apiv1.ResourceReference{Namespace: "team", Name: "assistant"},
		},
	}}
	_, err = client.ListAccessRoles(ctx, &apiv1.ListAccessRolesRequest{})
	assertGRPCCode(t, err, codes.Unauthenticated)
	roles, err := client.ListAccessRoles(admin, &apiv1.ListAccessRolesRequest{})
	require.NoError(t, err)
	require.Len(t, roles.Roles, 9)
	workspaces, err := client.ListWorkspaces(admin, &apiv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Equal(t, []*apiv1.WorkspaceAuthorizationReference{{Namespace: "team", Uid: "workspace-1"}}, workspaces.Workspaces)
	workspaces, err = client.ListWorkspaces(alice, &apiv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Empty(t, workspaces.Workspaces)
	workspaces, err = client.ListWorkspaces(admin, &apiv1.ListWorkspacesRequest{Workspace: workspace.GetWorkspace()})
	require.NoError(t, err)
	require.Equal(t, "workspace-1", workspaces.Workspaces[0].Uid)
	workspaces, err = client.ListWorkspaces(alice, &apiv1.ListWorkspacesRequest{Workspace: workspace.GetWorkspace()})
	require.NoError(t, err)
	require.Empty(t, workspaces.Workspaces)
	_, err = client.ListWorkspaces(admin, &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{Limit: 101}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = client.CheckPermissions(alice, &apiv1.CheckPermissionsRequest{})
	assertGRPCCode(t, err, codes.InvalidArgument)
	membership := &apiv1.GrantRoleRequest{Workspace: workspace.GetWorkspace(), RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{
		Subject: subject("alice"), Role: apiv1.AccessRole_ACCESS_ROLE_WORKSPACE_MEMBER, Scope: workspace,
	}}
	_, err = client.GrantRole(admin, membership)
	require.NoError(t, err)
	workspaces, err = client.ListWorkspaces(alice, &apiv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Equal(t, []*apiv1.WorkspaceAuthorizationReference{{Namespace: "team", Uid: "workspace-1"}}, workspaces.Workspaces)
	workspaces, err = client.ListWorkspaces(alice, &apiv1.ListWorkspacesRequest{Workspace: workspace.GetWorkspace()})
	require.NoError(t, err)
	require.Equal(t, []*apiv1.WorkspaceAuthorizationReference{{Namespace: "team", Uid: "workspace-1"}}, workspaces.Workspaces)
	workspaces, err = client.ListWorkspaces(alice, &apiv1.ListWorkspacesRequest{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team", Uid: "old-uid"},
	})
	require.NoError(t, err)
	require.Empty(t, workspaces.Workspaces)
	invoke := &apiv1.GrantRoleRequest{Workspace: workspace.GetWorkspace(), RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{
		Subject: subject("alice"), Role: apiv1.AccessRole_ACCESS_ROLE_EXECUTOR, Scope: agent,
	}}
	granted, err := client.GrantRole(admin, invoke)
	require.NoError(t, err)
	require.Equal(t, "agent-1", granted.Binding.Scope.GetKubernetes().Uid)
	check := &apiv1.CheckPermissionsRequest{Workspace: workspace.GetWorkspace(), Checks: []*apiv1.ResourceGrant{{
		Resource: agent, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_READ, apiv1.Permission_PERMISSION_AGENT_INVOKE},
	}}}
	access, err := client.CheckPermissions(alice, check)
	require.NoError(t, err)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_INVOKE}, access.Resources[0].AllowedActions)
	_, err = client.GrantRole(alice, &apiv1.GrantRoleRequest{Workspace: workspace.GetWorkspace(), RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{
		Subject: subject("alice"), Role: apiv1.AccessRole_ACCESS_ROLE_DEFINITION_EDITOR, Scope: agent,
	}})
	assertGRPCCode(t, err, codes.PermissionDenied)
	bindings, err := client.ListRoleBindings(admin, &apiv1.ListRoleBindingsRequest{Workspace: workspace.GetWorkspace(), Scope: agent})
	require.NoError(t, err)
	require.Len(t, bindings.Bindings, 1)
	require.Equal(t, subject("alice").String(), bindings.Bindings[0].Subject.String())
	_, err = client.RevokeRole(admin, &apiv1.RevokeRoleRequest{Workspace: workspace.GetWorkspace(), RequestId: uuid.NewString(), Binding: granted.Binding})
	require.NoError(t, err)
	// Replaying a confirmed grant retains its original result without restoring access.
	_, err = client.GrantRole(admin, invoke)
	require.NoError(t, err)
	access, err = client.CheckPermissions(alice, check)
	require.NoError(t, err)
	require.Empty(t, access.Resources[0].AllowedActions)
	outsider := identity("outsider", []string{})
	_, err = client.GrantRole(admin, &apiv1.GrantRoleRequest{Workspace: workspace.GetWorkspace(), RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{
		Subject: subject("outsider"), Role: apiv1.AccessRole_ACCESS_ROLE_EXECUTOR, Scope: agent,
	}})
	require.NoError(t, err)
	access, err = client.CheckPermissions(outsider, check)
	require.NoError(t, err)
	require.Empty(t, access.Resources[0].AllowedActions)

	group := &apiv1.SubjectReference{Subject: &apiv1.SubjectReference_Group{
		Group: &apiv1.IdentityGroup{Issuer: issuer.URL, Id: "ops"},
	}}
	for _, role := range []apiv1.AccessRole{apiv1.AccessRole_ACCESS_ROLE_WORKSPACE_MEMBER, apiv1.AccessRole_ACCESS_ROLE_EXECUTOR} {
		_, err = client.GrantRole(admin, &apiv1.GrantRoleRequest{Workspace: workspace.GetWorkspace(), RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{
			Subject: group, Role: role, Scope: workspace,
		}})
		require.NoError(t, err)
	}
	access, err = client.CheckPermissions(identity("machine", []string{"ops"}), check)
	require.NoError(t, err)
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_AGENT_INVOKE}, access.Resources[0].AllowedActions)
	workspaces, err = client.ListWorkspaces(identity("machine", []string{"ops"}), &apiv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Equal(t, []*apiv1.WorkspaceAuthorizationReference{{Namespace: "team", Uid: "workspace-1"}}, workspaces.Workspaces)
	access, err = client.CheckPermissions(identity("machine", []string{}), check)
	require.NoError(t, err)
	require.Empty(t, access.Resources[0].AllowedActions)
	forged := proto.CloneOf(group)
	forged.GetGroup().Issuer = "https://untrusted.example"
	_, err = client.GrantRole(admin, &apiv1.GrantRoleRequest{Workspace: workspace.GetWorkspace(), RequestId: uuid.NewString(), Binding: &apiv1.RoleBinding{
		Subject: forged, Role: apiv1.AccessRole_ACCESS_ROLE_EXECUTOR, Scope: workspace,
	}})
	assertGRPCCode(t, err, codes.InvalidArgument)

	for _, change := range []func(*apiv1.CheckPermissionsRequest){
		func(r *apiv1.CheckPermissionsRequest) { r.Workspace.Namespace = "other-team" },
		func(r *apiv1.CheckPermissionsRequest) { r.Workspace.Uid = "old-workspace" },
		func(r *apiv1.CheckPermissionsRequest) { r.Checks[0].Resource.GetKubernetes().Uid = "old-agent" },
	} {
		stale := proto.CloneOf(check)
		change(stale)
		access, err = client.CheckPermissions(identity("machine", []string{"ops"}), stale)
		require.NoError(t, err)
		require.Empty(t, access.Resources[0].AllowedActions)
	}
	page, err := client.ListRoleBindings(admin, &apiv1.ListRoleBindingsRequest{
		Workspace: workspace.GetWorkspace(), Scope: workspace, Page: &apiv1.PageRequest{Limit: 1},
	})
	require.NoError(t, err)
	require.Len(t, page.Bindings, 1)
	require.NotEmpty(t, page.Page.NextPageToken)
	_, err = client.ListRoleBindings(admin, &apiv1.ListRoleBindingsRequest{
		Workspace: workspace.GetWorkspace(), Scope: agent, Page: &apiv1.PageRequest{PageToken: page.Page.NextPageToken},
	})
	assertGRPCCode(t, err, codes.InvalidArgument)
}
