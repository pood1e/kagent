package executionauth

import (
	"context"
	"fmt"
	"slices"

	"github.com/kagent-dev/kagent/go/api/authorization"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// Policy applies atomic-scope requirements before the configured resource policy.
// Unconfigured resources and verbs fail closed when scope policy is enabled.
type Policy struct {
	Base   auth.CollectionAuthorizer
	Config *configv1.ExecutionAuthorizationConfig
}

var _ auth.CollectionAuthorizer = (*Policy)(nil)

func (p *Policy) permits(principal auth.Principal, verb auth.Verb, resourceType string) bool {
	if p.Config == nil {
		return true
	}
	for _, rule := range p.Config.Rules {
		if rule.ResourceType == resourceType && slices.Contains(rule.Verbs, string(verb)) {
			if len(rule.AnyScope) == 0 {
				return principal.ID != ""
			}
			for _, scope := range rule.AnyScope {
				if slices.Contains(principal.Scopes, scope) {
					return true
				}
			}
		}
	}
	return false
}
func (p *Policy) Check(ctx context.Context, principal auth.Principal, verb auth.Verb, resource auth.Resource) error {
	if !p.permits(principal, verb, resource.Type) {
		return fmt.Errorf("required execution scope is missing")
	}
	return p.Base.Check(ctx, principal, verb, resource)
}
func (p *Policy) Scope(ctx context.Context, principal auth.Principal, verb auth.Verb, resourceType string) (authorization.AuthorizationScope, error) {
	if !p.permits(principal, verb, resourceType) {
		return authorization.AuthorizationScope{Kind: authorization.ScopeNone}, nil
	}
	return p.Base.Scope(ctx, principal, verb, resourceType)
}
