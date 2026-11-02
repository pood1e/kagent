package grpcserver

import (
	"context"
	"testing"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"
	protovalidatemiddleware "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func TestAuthorizationRequestValidation(t *testing.T) {
	validator, err := protovalidate.New()
	require.NoError(t, err)
	require.NoError(t, validator.Validate(&apiv1.ListWorkspacesRequest{}))
	require.NoError(t, validator.Validate(&apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{Limit: 100}}))
	_, err = protovalidatemiddleware.UnaryServerInterceptor(validator)(
		t.Context(), &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{Limit: 101}}, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) {
			t.Fatal("invalid workspace query reached handler")
			return nil, nil
		},
	)
	assertGRPCCode(t, err, codes.InvalidArgument)
	valid := &apiv1.GrantRoleRequest{
		RequestId: uuid.NewString(), Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"},
		Binding: &apiv1.RoleBinding{
			Subject: &apiv1.SubjectReference{Subject: &apiv1.SubjectReference_PrincipalId{PrincipalId: "alice"}},
			Role:    apiv1.AccessRole_ACCESS_ROLE_EXECUTOR,
			Scope: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
				Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"},
			}},
		},
	}
	require.NoError(t, validator.Validate(valid))
	for _, test := range []struct {
		name   string
		change func(*apiv1.GrantRoleRequest)
	}{
		{"missing workspace", func(r *apiv1.GrantRoleRequest) { r.Workspace = nil }},
		{"invalid workspace", func(r *apiv1.GrantRoleRequest) { r.Workspace.Namespace = "NOT A NAMESPACE" }},
		{"missing request ID", func(r *apiv1.GrantRoleRequest) { r.RequestId = "" }},
		{"missing subject", func(r *apiv1.GrantRoleRequest) { r.Binding.Subject = nil }},
		{"missing scope", func(r *apiv1.GrantRoleRequest) { r.Binding.Scope = nil }},
		{"unknown role", func(r *apiv1.GrantRoleRequest) { r.Binding.Role = apiv1.AccessRole(100) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := proto.CloneOf(valid)
			test.change(request)
			_, err := protovalidatemiddleware.UnaryServerInterceptor(validator)(
				t.Context(), request, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
					t.Fatal("invalid authorization request reached handler")
					return nil, nil
				},
			)
			assertGRPCCode(t, err, codes.InvalidArgument)
		})
	}
}
