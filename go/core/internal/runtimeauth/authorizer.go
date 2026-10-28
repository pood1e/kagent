package runtimeauth

import (
	"context"
	"fmt"
	"slices"

	"github.com/kagent-dev/kagent/go/api/authorization"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/authscope"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// Authorizer applies configured policy within the verified actor's native
// resource boundary for HTTP, MCP, and gRPC callers.
type Authorizer struct {
	users     auth.CollectionAuthorizer
	store     SessionStore
	authority Authority
}

var _ auth.CollectionAuthorizer = (*Authorizer)(nil)

func NewAuthorizer(users auth.CollectionAuthorizer, store SessionStore, authority Authority) *Authorizer {
	return &Authorizer{users: users, store: store, authority: authority}
}

// Authority resolves current initiator policy independently of Actor identity.
type Authority interface {
	Resolve(context.Context, string) (auth.Session, error)
	Session(context.Context, string) (*apiv1.Session, error)
}

func (a *Authorizer) Check(ctx context.Context, principal auth.Principal, verb auth.Verb, resource auth.Resource) error {
	if cleanup, ok := cancellationFrom(ctx); ok {
		if resource.Type == "Session" && resource.Name == cleanup.SessionID && (verb == auth.VerbGet || verb == auth.VerbUpdate) {
			return nil
		}
		return fmt.Errorf("cancellation recovery is restricted to its owning Session")
	}
	if principal.RuntimeBinding == nil {
		if principal.Execution != nil {
			if a.authority == nil {
				return fmt.Errorf("native execution authority is required")
			}
			target, err := a.authority.Session(ctx, principal.Execution.SessionId)
			if err != nil || !sessionCredentialResource(target, verb, resource) {
				return fmt.Errorf("session credential resource boundary denied")
			}
			resolved, err := a.authority.Resolve(ctx, principal.Execution.SessionId)
			if err != nil {
				return err
			}
			narrowed := resolved.Principal()
			narrowed.Scopes = scopeIntersection(narrowed.Scopes, principal.Scopes)
			principal = narrowed
		}
		return a.users.Check(ctx, principal, verb, resource)
	}
	session, err := Session(ctx, a.store)
	if err != nil {
		return err
	}
	allowed := runtimeResourceAllowed(session, verb, resource)
	// Persistence is the bounded cleanup path after revocation. The store fences
	// new work and still accepts terminal cleanup from the live, recorded Actor.
	if resource.Type == "TaskStore" {
		if !allowed {
			return fmt.Errorf("actor persistence boundary denied")
		}
		return a.users.Check(ctx, principal, verb, resource)
	}
	if a.authority == nil {
		if !allowed {
			return fmt.Errorf("native execution authority is required")
		}
		return a.users.Check(ctx, principal, verb, resource)
	}
	resolved, err := a.authority.Resolve(ctx, session.Id)
	if err != nil {
		return err
	}
	narrowed := resolved.Principal()
	principal.Scopes, principal.Execution, principal.Claims = narrowed.Scopes, narrowed.Execution, narrowed.Claims
	if origin, ok := authSessionOrigin(ctx); ok {
		principal.Scopes = scopeIntersection(principal.Scopes, origin.Scopes)
	}
	if !allowed {
		switch resource.Type {
		case "Agent":
			allowed = verb == auth.VerbGet && resource.Namespace == session.GetAgent().GetRef().GetNamespace()
		case "Session":
			if resource.Name == "" {
				allowed = verb == auth.VerbCreate || verb == auth.VerbGet
				break
			}
			if verb != auth.VerbGet && verb != auth.VerbCreate && verb != auth.VerbUpdate {
				break
			}
			target, err := a.authority.Session(ctx, resource.Name)
			if err != nil {
				return fmt.Errorf("actor conversation boundary denied")
			}
			targetAuthority, err := a.authority.Resolve(ctx, target.Id)
			allowed = err == nil && target.Creator == session.Creator && target.GetAgent().GetRef().GetNamespace() == session.GetAgent().GetRef().GetNamespace() && targetAuthority.Principal().Execution.RootSessionId == principal.Execution.RootSessionId
		}
	}
	if !allowed {
		return fmt.Errorf("actor is not authorized for %s on %s", verb, resource.Type)
	}
	return a.users.Check(ctx, principal, verb, resource)
}

func runtimeResourceAllowed(session *apiv1.Session, verb auth.Verb, resource auth.Resource) bool {
	switch resource.Type {
	case "Agent":
		if verb == auth.VerbGet && resource.Namespace == session.GetAgent().GetRef().GetNamespace() && resource.Name == session.GetAgent().GetRef().GetName() {
			return true
		}
	case "Session":
		// Collection authorization grants no individual Session access; the service
		// checks each row before pagination. Empty create never grants a new Session.
		if (verb == auth.VerbGet || verb == auth.VerbList) && resource.Name == "" {
			return true
		}
		if resource.Name == session.Id && (verb == auth.VerbGet || verb == auth.VerbCreate || verb == auth.VerbUpdate) {
			return true
		}
	case "ToolInventory":
		return verb == auth.VerbUpdate && resource.Namespace == session.GetAgent().GetRef().GetNamespace() && resource.Name == session.Id
	case "TaskStore":
		return resource.Name == session.Id && resource.Namespace == session.GetAgent().GetRef().GetNamespace() &&
			(verb == auth.VerbGet || verb == auth.VerbList || verb == auth.VerbCreate || verb == auth.VerbUpdate)
	case "Memory":
		if resource.Name == session.GetAgent().GetRef().GetNamespace()+"/"+session.GetAgent().GetRef().GetName() &&
			(verb == auth.VerbGet || verb == auth.VerbList || verb == auth.VerbCreate || verb == auth.VerbDelete) {
			return true
		}
	}
	return false
}

func (a *Authorizer) Scope(ctx context.Context, principal auth.Principal, verb auth.Verb, resourceType string) (authorization.AuthorizationScope, error) {
	if principal.RuntimeBinding == nil {
		if principal.Execution != nil {
			return authorization.AuthorizationScope{Kind: authorization.ScopeNone}, nil
		}
		return a.users.Scope(ctx, principal, verb, resourceType)
	}

	session, err := Session(ctx, a.store)
	if err != nil {
		return authorization.AuthorizationScope{}, err
	}
	if verb != auth.VerbList || resourceType != "Agent" {
		return authorization.AuthorizationScope{Kind: authorization.ScopeNone}, nil
	}
	if a.authority != nil {
		resolved, err := a.authority.Resolve(ctx, session.Id)
		if err != nil {
			return authorization.AuthorizationScope{}, err
		}
		narrowed := resolved.Principal()
		principal.Scopes, principal.Execution, principal.Claims = narrowed.Scopes, narrowed.Execution, narrowed.Claims
		if origin, ok := authSessionOrigin(ctx); ok {
			principal.Scopes = scopeIntersection(principal.Scopes, origin.Scopes)
		}
	}
	scope, err := a.users.Scope(ctx, principal, verb, resourceType)
	if err != nil {
		return authorization.AuthorizationScope{}, err
	}
	matcher, err := authscope.CompileScope(scope, authorization.AttributeNamespace)
	if err != nil {
		return authorization.AuthorizationScope{}, err
	}
	namespace := session.GetAgent().GetRef().GetNamespace()
	// Apply the native team boundary to every clause rather than broadening a
	// configured object scope. The existing matcher validates the original scope.
	if a.authority != nil {
		if scope.Kind == authorization.ScopeNone {
			return scope, nil
		}
		predicate := authorization.ScopePredicate{Attribute: authorization.AttributeNamespace, Operator: authorization.ScopeIn, Values: []string{namespace}}
		if scope.Kind == authorization.ScopeAll {
			return authorization.AuthorizationScope{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{All: []authorization.ScopePredicate{predicate}}}}, nil
		}
		for i := range scope.AnyOf {
			scope.AnyOf[i].All = append(scope.AnyOf[i].All, predicate)
		}
		return scope, nil
	}
	if !matcher.Matches(namespace, session.GetAgent().GetRef().GetName()) {
		return authorization.AuthorizationScope{Kind: authorization.ScopeNone}, nil
	}
	return authorization.AuthorizationScope{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{All: []authorization.ScopePredicate{
		{Attribute: authorization.AttributeNamespace, Operator: authorization.ScopeIn, Values: []string{namespace}},
		{Attribute: authorization.AttributeName, Operator: authorization.ScopeIn, Values: []string{session.GetAgent().GetRef().GetName()}},
	}}}}, nil
}

func scopeIntersection(scopes, ceiling []string) []string {
	var result []string
	for _, scope := range scopes {
		if slices.Contains(ceiling, scope) {
			result = append(result, scope)
		}
	}
	return result
}

func sessionCredentialResource(session *apiv1.Session, verb auth.Verb, resource auth.Resource) bool {
	if session == nil {
		return false
	}
	switch resource.Type {
	case "Session":
		return resource.Name == session.Id && (verb == auth.VerbGet || verb == auth.VerbCreate || verb == auth.VerbUpdate)
	case "Agent":
		return verb == auth.VerbGet && resource.Namespace == session.GetAgent().GetRef().GetNamespace() && resource.Name == session.GetAgent().GetRef().GetName()
	}
	return false
}
