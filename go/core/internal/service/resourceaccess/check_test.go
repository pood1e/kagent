package resourceaccess_test

import (
	"context"
	"fmt"
	"testing"

	"buf.build/go/protovalidate"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/resourceaccess"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type session struct{}

func (session) Principal() auth.Principal { return auth.Principal{ID: "reader"} }

type checker func(*apiv1.CheckPermissionsRequest) *apiv1.CheckPermissionsResponse

func (c checker) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	return c(request), nil
}

func TestCheckRejectsIncompleteOrBroadenedDecision(t *testing.T) {
	ctx := auth.AuthSessionTo(t.Context(), session{})
	resource := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "team"},
	}}
	checks := []*apiv1.ResourceGrant{{Resource: resource, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_HARNESS_READ}}}
	for _, test := range []struct {
		name string
		body checker
	}{
		{"incomplete", func(*apiv1.CheckPermissionsRequest) *apiv1.CheckPermissionsResponse {
			return &apiv1.CheckPermissionsResponse{}
		}},
		{"missing resource", func(*apiv1.CheckPermissionsRequest) *apiv1.CheckPermissionsResponse {
			return &apiv1.CheckPermissionsResponse{Resources: []*apiv1.ResourceAccess{{}}}
		}},
		{"unexpected action", func(*apiv1.CheckPermissionsRequest) *apiv1.CheckPermissionsResponse {
			return &apiv1.CheckPermissionsResponse{Resources: []*apiv1.ResourceAccess{{Resource: resource,
				AllowedActions: []apiv1.Permission{apiv1.Permission_PERMISSION_HARNESS_MANAGE}}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resourceaccess.Check(ctx, test.body, "team", checks)
			require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable), "Check() error = %v", err)
		})
	}
	_, err := resourceaccess.Check(t.Context(), checker(func(*apiv1.CheckPermissionsRequest) *apiv1.CheckPermissionsResponse {
		t.Fatal("unauthenticated request reached checker")
		return nil
	}), "team", checks)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnauthenticated))
}

func TestCheckSplitsRequestsAtTheAPILimit(t *testing.T) {
	ctx := auth.AuthSessionTo(t.Context(), session{})
	var checks []*apiv1.ResourceGrant
	for i := range 2*auth.MaxPermissionChecks + 1 {
		checks = append(checks, &apiv1.ResourceGrant{
			Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
				Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: fmt.Sprintf("team-%d", i)},
			}},
			Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_HARNESS_READ},
		})
	}
	var sizes []int
	result, err := resourceaccess.Check(ctx, checker(func(request *apiv1.CheckPermissionsRequest) *apiv1.CheckPermissionsResponse {
		require.NoError(t, protovalidate.Validate(request))
		sizes = append(sizes, len(request.Checks))
		response := &apiv1.CheckPermissionsResponse{}
		for _, check := range request.Checks {
			response.Resources = append(response.Resources, &apiv1.ResourceAccess{Resource: proto.CloneOf(check.Resource)})
		}
		return response
	}), "team", checks)
	require.NoError(t, err)
	require.Len(t, result, len(checks))
	require.Equal(t, []int{auth.MaxPermissionChecks, auth.MaxPermissionChecks, 1}, sizes)
}
