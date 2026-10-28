package memory

import (
	"context"

	"k8s.io/apimachinery/pkg/util/validation"

	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// managementScope binds personal data to its authenticated owner and team data
// to an explicitly authorized namespace. Runtime callers stay within their team.
func (s *Service) managementScope(ctx context.Context, verb auth.Verb, requested *runtimev1.MemoryScope) (*runtimev1.MemoryScope, string, error) {
	if _, shared := auth.ShareContextFrom(ctx); shared {
		return nil, "", serviceerrors.NewPermissionDenied("Memory requires independent authorization", nil)
	}
	caller, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return nil, "", serviceerrors.NewUnauthenticated("Authenticated identity is required", nil)
	}
	scope, owner := requested, caller.Principal().ID
	if caller.Principal().RuntimeBinding != nil {
		if s.store == nil {
			return nil, "", serviceerrors.NewInternal("Memory store is not configured", nil)
		}
		session, err := runtimeauth.Session(ctx, s.store)
		if err != nil {
			return nil, "", err
		}
		owner = session.Creator
		if requested.GetTeamNamespace() != "" {
			if requested.GetTeamNamespace() != session.GetAgent().GetNamespace() {
				return nil, "", serviceerrors.NewPermissionDenied("Memory namespace is outside the runtime team", nil)
			}
		} else {
			scope = &runtimev1.MemoryScope{Target: &runtimev1.MemoryScope_AgentName{AgentName: session.GetAgent().GetNamespace() + "/" + session.GetAgent().GetName()}}
		}
	}
	resource := auth.Resource{Type: "Memory", Name: scope.GetAgentName()}
	if team := scope.GetTeamNamespace(); team != "" {
		if len(validation.IsDNS1123Label(team)) != 0 {
			return nil, "", serviceerrors.NewInvalidArgument("Invalid team namespace", nil)
		}
		resource = auth.Resource{Type: "TeamMemory", Namespace: team}
		owner = ""
	} else if scope.GetAgentName() == "" || owner == "" {
		return nil, "", serviceerrors.NewInvalidArgument("Personal memory requires an agent and owner", nil)
	}
	if err := s.authorizer.Check(ctx, caller.Principal(), verb, resource); err != nil {
		return nil, "", serviceerrors.NewPermissionDenied("Memory access denied", err)
	}
	return scope, owner, nil
}
