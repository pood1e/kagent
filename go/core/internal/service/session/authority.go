package session

import (
	"context"
	"slices"

	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func authPrincipal(ctx context.Context) auth.Principal {
	session, _ := auth.AuthSessionFrom(ctx)
	return session.Principal()
}

// ExecutionContext reconstructs the original narrowed source for durable workers.
func (s *Service) ExecutionContext(ctx context.Context, id string) (context.Context, error) {
	if s.authority == nil {
		return ctx, nil
	}
	session, err := s.authority.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	return auth.AuthSessionTo(ctx, session), nil
}

func (s *Service) sendAuthority(ctx context.Context, id string, continuation bool) (auth.Session, error) {
	caller, _ := auth.AuthSessionFrom(ctx)
	if s.authority == nil {
		return caller, nil
	}
	resolved, err := s.authority.Resolve(ctx, id)
	if err != nil {
		return nil, serviceerrors.NewPermissionDenied("Execution authority unavailable", err)
	}
	principal := resolved.Principal()
	original := caller.Principal()
	ceiling := original.Scopes
	if binding := original.RuntimeBinding; binding != nil {
		origin, err := s.authority.Resolve(ctx, binding.SessionID)
		if err != nil {
			return nil, serviceerrors.NewPermissionDenied("Originating authority unavailable", err)
		}
		if origin.Principal().Execution.RootSessionId != principal.Execution.RootSessionId {
			return nil, serviceerrors.NewPermissionDenied("Execution root mismatch", nil)
		}
		ceiling = origin.Principal().Scopes
		if original.ID == principal.ID {
			ceiling = slices.DeleteFunc(slices.Clone(ceiling), func(scope string) bool { return !slices.Contains(original.Scopes, scope) })
		}
	} else if original.ID != principal.ID {
		return nil, serviceerrors.NewPermissionDenied("Execution initiator mismatch", nil)
	}
	principal.Scopes = slices.DeleteFunc(slices.Clone(principal.Scopes), func(scope string) bool { return !slices.Contains(ceiling, scope) })
	source, _ := resolved.(auth.TokenSource)
	narrowed := auth.NewCredentialSession(principal, source)
	verb := auth.VerbCreate
	if continuation {
		verb = auth.VerbUpdate
	}
	if err := s.authorizer.Check(auth.AuthSessionTo(ctx, narrowed), principal, verb, auth.Resource{Type: "Session", Name: id}); err != nil {
		return nil, serviceerrors.NewPermissionDenied("Execution scope denied", err)
	}
	return narrowed, nil
}

// RevokeExecutionAuthority requires the Session's mutation permission as well
// as independent initiator authentication; shares and Actors fail closed.
func (s *Service) RevokeExecutionAuthority(ctx context.Context, id string) error {
	if _, err := s.getAuthorized(ctx, id, auth.VerbUpdate); err != nil {
		return err
	}
	if s.authority == nil {
		return serviceerrors.NewFailedPrecondition("Execution authority is not configured", nil)
	}
	if err := s.authority.RevokeAuthority(ctx, id); err != nil {
		return serviceerrors.NewPermissionDenied("Cannot revoke execution authority", err)
	}
	return nil
}
