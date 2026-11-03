// Package resourceaccess evaluates native resource permissions for control-plane services.
package resourceaccess

import (
	"context"
	"slices"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type Checker interface {
	CheckPermissions(context.Context, *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error)
}

// Check evaluates a complete batch without accepting partial or broadened decisions.
func Check(ctx context.Context, checker Checker, namespace string, checks []*apiv1.ResourceGrant) ([]*apiv1.ResourceAccess, error) {
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session == nil || session.Principal().ID == "" {
		return nil, serviceerrors.NewUnauthenticated("Authenticated principal required", nil)
	}
	if checker == nil {
		return nil, serviceerrors.NewUnavailable("Resource authorization is not configured", nil)
	}
	result := make([]*apiv1.ResourceAccess, 0, len(checks))
	for start := 0; start < len(checks); start += 100 {
		batch := checks[start:min(start+100, len(checks))]
		response, err := checker.CheckPermissions(ctx, &apiv1.CheckPermissionsRequest{
			Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: namespace}, Checks: batch,
		})
		if err != nil {
			return nil, err
		}
		if response == nil || len(response.Resources) != len(batch) {
			return nil, serviceerrors.NewUnavailable("Incomplete resource authorization response", nil)
		}
		for i, access := range response.Resources {
			if access == nil || access.Resource == nil {
				return nil, serviceerrors.NewUnavailable("Invalid resource authorization response", nil)
			}
			for _, permission := range access.AllowedActions {
				if !slices.Contains(batch[i].Permissions, permission) {
					return nil, serviceerrors.NewUnavailable("Unexpected resource authorization action", nil)
				}
			}
		}
		result = append(result, response.Resources...)
	}
	return result, nil
}

// Require returns the canonical resource identity associated with a permitted action.
func Require(ctx context.Context, checker Checker, namespace string, resource *apiv1.AuthorizationResource, permission apiv1.Permission) (*apiv1.AuthorizationResource, error) {
	access, err := Check(ctx, checker, namespace, []*apiv1.ResourceGrant{{Resource: resource, Permissions: []apiv1.Permission{permission}}})
	if err != nil {
		return nil, err
	}
	if !slices.Contains(access[0].AllowedActions, permission) {
		return nil, serviceerrors.NewPermissionDenied("Resource access denied", nil)
	}
	return proto.CloneOf(access[0].Resource), nil
}
