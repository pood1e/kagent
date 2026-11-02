package authorization

import (
	"context"
	"slices"
	"strconv"

	"buf.build/go/protovalidate"
	fga "github.com/openfga/go-sdk"
	fgaclient "github.com/openfga/go-sdk/client"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type checkedPermission struct {
	resource   *resolvedResource
	permission apiv1.Permission
	result     int
}

type relationCheck struct {
	object   string
	relation string
	facts    []fga.TupleKey
}

func (s *Service) CheckPermissions(ctx context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	principal, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	return s.checkPermissions(ctx, principal, request)
}

// CheckExecutionPermissions evaluates a trusted, live initiator reconstructed by
// executionauth.Resolve and intersects it with its durable resource ceiling.
// This internal service boundary is separate from authorization-management RPCs.
func (s *Service) CheckExecutionPermissions(ctx context.Context, principal auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	grant := principal.Execution
	if grant == nil || grant.GetScopeCeiling().GetResources() == nil || principal.ID != grant.InitiatorId ||
		grant.WorkspaceUid == "" || request.GetWorkspace().GetUid() != grant.WorkspaceUid || principal.RuntimeBinding != nil {
		return nil, serviceerrors.NewPermissionDenied("Bound execution initiator required", nil)
	}
	response, err := s.checkPermissions(ctx, principal, request)
	if err != nil {
		return nil, err
	}
	for _, result := range response.Resources {
		result.AllowedActions = slices.DeleteFunc(result.AllowedActions, func(permission apiv1.Permission) bool {
			return !auth.ResourceGrantAllows(grant.ScopeCeiling.Resources, result.Resource, permission)
		})
	}
	return response, nil
}

func (s *Service) checkPermissions(ctx context.Context, principal auth.Principal, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	if err := protovalidate.Validate(request); err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid permission checks", err)
	}
	response := &apiv1.CheckPermissionsResponse{}
	var checks []checkedPermission
	for i, check := range request.Checks {
		result := &apiv1.ResourceAccess{Resource: proto.CloneOf(check.Resource)}
		response.Resources = append(response.Resources, result)
		resource, err := s.resolver.resolve(ctx, check.Resource)
		if serviceerrors.IsCode(err, serviceerrors.CodeNotFound) || serviceerrors.IsCode(err, serviceerrors.CodeFailedPrecondition) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !matchesWorkspace(resource, request.Workspace) {
			continue
		}
		for _, permission := range check.Permissions {
			if _, err := permissionRelation(resource.kind, permission); err != nil {
				return nil, serviceerrors.NewInvalidArgument("Permission does not apply to resource", err)
			}
			checks = append(checks, checkedPermission{resource: resource, permission: permission, result: i})
		}
	}
	allowed, err := s.batchCheck(ctx, principal, checks)
	if err != nil {
		return nil, err
	}
	for i, check := range checks {
		if allowed[i] {
			result := response.Resources[check.result]
			result.Resource = proto.CloneOf(check.resource.scope)
			result.AllowedActions = append(result.AllowedActions, check.permission)
		}
	}
	return response, nil
}

func (s *Service) batchCheck(ctx context.Context, principal auth.Principal, checks []checkedPermission) ([]bool, error) {
	items := make([]relationCheck, 0, len(checks))
	for _, check := range checks {
		relation, err := permissionRelation(check.resource.kind, check.permission)
		if err != nil {
			return nil, serviceerrors.NewInvalidArgument("Permission does not apply to resource", err)
		}
		items = append(items, relationCheck{object: check.resource.object, relation: relation, facts: check.resource.facts})
	}
	return s.batchRelations(ctx, principal, items)
}

func (s *Service) batchRelations(ctx context.Context, principal auth.Principal, checks []relationCheck) ([]bool, error) {
	if len(checks) == 0 {
		return nil, nil
	}
	user, err := principalSubject(principal.ID)
	if err != nil {
		return nil, serviceerrors.NewUnauthenticated("Invalid authenticated identity", err)
	}
	groups, err := groupContext(principal, s.config.GroupClaim)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Invalid authenticated groups", err)
	}
	body := fgaclient.ClientBatchCheckRequest{}
	for i, check := range checks {
		body.Checks = append(body.Checks, fgaclient.ClientBatchCheckItem{
			User: user, Relation: check.relation, Object: check.object, CorrelationId: strconv.Itoa(i),
			ContextualTuples: append(slices.Clone(groups), check.facts...),
		})
	}
	response, err := s.client.BatchCheck(ctx).Body(body).Options(fgaclient.BatchCheckOptions{
		Consistency:  new(fga.CONSISTENCYPREFERENCE_HIGHER_CONSISTENCY),
		MaxBatchSize: new(int32(50)), MaxParallelRequests: new(int32(4)),
	}).Execute()
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Authorization evaluation unavailable", err)
	}
	results := response.GetResult()
	if len(results) != len(checks) {
		return nil, serviceerrors.NewUnavailable("Incomplete authorization evaluation", nil)
	}
	allowed := make([]bool, len(checks))
	for i := range checks {
		result, ok := results[strconv.Itoa(i)]
		if !ok || result.HasError() || !result.HasAllowed() {
			return nil, serviceerrors.NewUnavailable("Authorization evaluation failed", nil)
		}
		allowed[i] = result.GetAllowed()
	}
	return allowed, nil
}

func (s *Service) check(ctx context.Context, principal auth.Principal, resource *resolvedResource, permission apiv1.Permission) (bool, error) {
	allowed, err := s.batchCheck(ctx, principal, []checkedPermission{{resource: resource, permission: permission}})
	if err != nil {
		return false, err
	}
	return allowed[0], nil
}
