package session

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/kagent-dev/kagent/go/api/authorization"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// CatalogReadScope authorizes a native published-task collection. A live anchor
// grants only its authorized forest, independently of a deleted root's lifecycle.
func (s *Service) CatalogReadScope(ctx context.Context, request *apiv1.TaskCatalogScope) (database.CatalogScope, error) {
	result := database.CatalogScope{}
	if request == nil {
		return result, serviceerrors.NewInvalidArgument("A catalog scope is required", nil)
	}
	result.Namespace = request.Namespace
	caller, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return result, serviceerrors.NewUnauthenticated("Authentication is required", nil)
	}
	uid, err := s.workspaceUID(ctx, request.Namespace)
	if err != nil {
		return result, err
	}
	result.WorkspaceUID = uid
	principal := caller.Principal()
	if share, shared := auth.ShareContextFrom(ctx); shared {
		if principal.RuntimeBinding != nil || principal.Execution != nil {
			return result, serviceerrors.NewPermissionDenied("Bound credentials cannot use shared catalog access", nil)
		}
		if _, other := request.Visibility.(*apiv1.TaskCatalogScope_OtherInitiators); other {
			return result, serviceerrors.NewPermissionDenied("A share grants one Session", nil)
		}
		if anchor := request.GetSessionTreeAnchor(); anchor != "" && anchor != share.SessionID {
			return result, serviceerrors.NewPermissionDenied("A share grants one Session", nil)
		}
		session, err := s.getAuthorized(ctx, share.SessionID, auth.VerbGet)
		if err != nil {
			return result, err
		}
		result.OwnerID = share.UserID
		result.SessionIDs = []string{session.Id}
		result.AnchorSessionID = session.Id
		return result, nil
	}
	if principal.RuntimeBinding != nil || principal.Execution != nil {
		return s.boundCatalogScope(ctx, request, principal, uid)
	}
	owner, err := s.authorize(ctx, auth.VerbGet, "")
	if err != nil {
		return result, err
	}
	collection, ok := s.authorizer.(auth.CollectionAuthorizer)
	if !ok {
		return result, serviceerrors.NewInternal("Session collection authorization is required", nil)
	}
	policy, err := collection.Scope(ctx, principal, auth.VerbGet, "Session")
	if err != nil {
		return result, serviceerrors.NewPermissionDenied("Session collection access denied", err)
	}
	result.SessionIDs, err = catalogSessionNames(policy)
	if err != nil {
		return result, serviceerrors.NewPermissionDenied("Session collection scope is invalid", err)
	}
	switch request.Visibility.(type) {
	case *apiv1.TaskCatalogScope_Owned:
		result.OwnerID = owner
		if owner == "" {
			result.SessionIDs = []string{}
		}
	case *apiv1.TaskCatalogScope_OtherInitiators:
		if _, err := s.authorizeResource(ctx, auth.VerbGet, auth.Resource{Type: "SessionAllCreators"}); err != nil {
			return result, err
		}
		result.OtherInitiatorID = principal.ID
	case *apiv1.TaskCatalogScope_SessionTreeAnchor:
		anchor, err := s.getAuthorized(ctx, request.GetSessionTreeAnchor(), auth.VerbGet)
		if err != nil {
			return result, err
		}
		result.AnchorSessionID = anchor.Id
		result.RootSessionID = anchor.GetAuthority().GetRootSessionId()
		if result.RootSessionID == "" {
			result.RootSessionID = anchor.Id
		}
	default:
		return result, serviceerrors.NewInvalidArgument("A catalog visibility is required", nil)
	}
	return result, nil
}

func (s *Service) boundCatalogScope(ctx context.Context, request *apiv1.TaskCatalogScope, principal auth.Principal, uid string) (database.CatalogScope, error) {
	result := database.CatalogScope{Namespace: request.Namespace, WorkspaceUID: uid}
	id := principal.Execution.GetSessionId()
	if principal.RuntimeBinding != nil {
		id = principal.RuntimeBinding.SessionID
	}
	if _, other := request.Visibility.(*apiv1.TaskCatalogScope_OtherInitiators); other || id == "" {
		return result, serviceerrors.NewPermissionDenied("A bound credential grants one Session", nil)
	}
	if anchor := request.GetSessionTreeAnchor(); anchor != "" && anchor != id {
		return result, serviceerrors.NewPermissionDenied("A bound credential grants one Session", nil)
	}
	session, err := s.getAuthorized(ctx, id, auth.VerbGet)
	if err != nil {
		return result, err
	}
	result.OwnerID = session.Creator
	result.AnchorSessionID = session.Id
	result.SessionIDs = []string{session.Id}
	return result, nil
}

// RenewCatalogRead reuses human delegation renewal once per authorized collection;
// share and bound credentials never acquire broader authority through a read.
func (s *Service) RenewCatalogRead(ctx context.Context) error {
	if s.authority == nil {
		return nil
	}
	caller, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return serviceerrors.NewUnauthenticated("Authentication is required", nil)
	}
	principal := caller.Principal()
	if _, shared := auth.ShareContextFrom(ctx); shared || principal.RuntimeBinding != nil || principal.Execution != nil {
		return nil
	}
	if err := s.authority.Renew(ctx); err != nil {
		return serviceerrors.NewPermissionDenied("Cannot renew execution credential", err)
	}
	return nil
}

// Session object checks use the Session UUID as name, without a namespace.
func catalogSessionNames(scope authorization.AuthorizationScope) ([]string, error) {
	switch scope.Kind {
	case authorization.ScopeAll, authorization.ScopeNone:
		if len(scope.AnyOf) != 0 {
			return nil, fmt.Errorf("a terminal scope contains clauses")
		}
		if scope.Kind == authorization.ScopeAll {
			return nil, nil
		}
		return []string{}, nil
	case authorization.ScopeAnyOf:
		if len(scope.AnyOf) == 0 {
			return nil, fmt.Errorf("an any-of scope requires clauses")
		}
	default:
		return nil, fmt.Errorf("unknown scope kind %q", scope.Kind)
	}
	names := map[string]bool{}
	for _, clause := range scope.AnyOf {
		if len(clause.All) == 0 {
			return nil, fmt.Errorf("an authorization clause requires predicates")
		}
		var selected []string
		for index, predicate := range clause.All {
			if predicate.Attribute != authorization.AttributeName || predicate.Operator != authorization.ScopeIn || len(predicate.Values) == 0 {
				return nil, fmt.Errorf("unsupported Session scope predicate")
			}
			values := make([]string, 0, len(predicate.Values))
			for _, name := range predicate.Values {
				id, err := uuid.Parse(name)
				if err != nil {
					return nil, fmt.Errorf("invalid Session scope name: %w", err)
				}
				values = append(values, id.String())
			}
			if index == 0 {
				selected = values
			} else {
				selected = intersectCatalogNames(selected, values)
			}
		}
		for _, name := range selected {
			names[name] = true
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	slices.Sort(result)
	return result, nil
}

func intersectCatalogNames(current, allowed []string) []string {
	if current == nil {
		return slices.Clone(allowed)
	}
	result := []string{}
	for _, id := range current {
		if slices.Contains(allowed, id) {
			result = append(result, id)
		}
	}
	return result
}
