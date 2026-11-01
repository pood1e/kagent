package session

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/kagent-dev/kagent/go/api/authorization"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

const catalogSessionID = "11111111-1111-4111-8111-111111111111"
const catalogRootID = "22222222-2222-4222-8222-222222222222"

type catalogIdentity struct{ principal auth.Principal }

func (i catalogIdentity) Principal() auth.Principal { return i.principal }

type catalogAuthorizer struct {
	scope  authorization.AuthorizationScope
	deny   string
	checks []auth.Resource
}

func (a *catalogAuthorizer) Check(_ context.Context, _ auth.Principal, _ auth.Verb, resource auth.Resource) error {
	a.checks = append(a.checks, resource)
	if resource.Type == a.deny {
		return errors.New("denied")
	}
	return nil
}

func (a *catalogAuthorizer) Scope(context.Context, auth.Principal, auth.Verb, string) (authorization.AuthorizationScope, error) {
	return a.scope, nil
}

func ownedCatalogScope() *apiv1.TaskCatalogScope {
	return &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_Owned{Owned: &emptypb.Empty{}}}
}

func TestCatalogScopePreservesOwnerAndObjectPolicy(t *testing.T) {
	policy := &catalogAuthorizer{scope: authorization.AuthorizationScope{Kind: authorization.ScopeAll}}
	s := NewService(&serviceTestStore{}, policy, nil)
	result, err := s.CatalogReadScope(serviceTestContext("alice"), ownedCatalogScope())
	require.NoError(t, err)
	require.Equal(t, "alice", result.OwnerID)
	require.Nil(t, result.SessionIDs)
	policy.scope.Kind = authorization.ScopeNone
	result, err = s.CatalogReadScope(serviceTestContext("alice"), ownedCatalogScope())
	require.NoError(t, err)
	require.NotNil(t, result.SessionIDs)
	require.Empty(t, result.SessionIDs)

	result, err = s.CatalogReadScope(auth.AuthSessionTo(t.Context(), auth.ControlPlaneSession{}), ownedCatalogScope())
	require.NoError(t, err)
	require.NotNil(t, result.SessionIDs, "an empty identity must not turn owned visibility into all creators")

	_, err = s.CatalogReadScope(t.Context(), ownedCatalogScope())
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnauthenticated))
	s = NewService(&serviceTestStore{}, serviceTestAuthorizer{}, nil)
	_, err = s.CatalogReadScope(serviceTestContext("alice"), ownedCatalogScope())
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInternal), "collection policy is mandatory for an unbound collection")
}

func TestCatalogScopeUsesLiveAnchorInsteadOfDeletedRoot(t *testing.T) {
	store := &serviceTestStore{getResult: &apiv1.Session{Id: catalogSessionID, Creator: "alice", Authority: &apiv1.SessionAuthority{RootSessionId: catalogRootID}}}
	policy := &catalogAuthorizer{scope: authorization.AuthorizationScope{Kind: authorization.ScopeAll}}
	s := NewService(store, policy, nil)
	request := &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_SessionTreeAnchor{SessionTreeAnchor: catalogSessionID}}
	result, err := s.CatalogReadScope(serviceTestContext("alice"), request)
	require.NoError(t, err)
	require.Equal(t, catalogSessionID, store.getID, "only the live child is loaded and authorized")
	require.Equal(t, catalogRootID, result.RootSessionID, "immutable lineage remains the SQL tree boundary")
	require.Equal(t, catalogSessionID, result.AnchorSessionID)
	require.Empty(t, result.OwnerID, "authorized trees can contain delegated storage partitions")
	for _, check := range policy.checks {
		require.NotEqual(t, catalogRootID, check.Name)
	}
}

func TestCatalogScopeOtherInitiatorsRequiresSeparateGrant(t *testing.T) {
	policy := &catalogAuthorizer{scope: authorization.AuthorizationScope{Kind: authorization.ScopeAll}, deny: "SessionAllCreators"}
	s := NewService(&serviceTestStore{}, policy, nil)
	request := &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_OtherInitiators{OtherInitiators: &emptypb.Empty{}}}
	_, err := s.CatalogReadScope(serviceTestContext("alice"), request)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	policy.deny = ""
	result, err := s.CatalogReadScope(serviceTestContext("alice"), request)
	require.NoError(t, err)
	require.Empty(t, result.OwnerID)
	require.Equal(t, "alice", result.OtherInitiatorID)
}

func TestCatalogScopeSharedAndBoundCredentialsGrantOneSession(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal auth.Principal
		share     bool
	}{
		{name: "share", principal: auth.Principal{ID: "visitor"}, share: true},
		{name: "actor", principal: auth.Principal{ID: "actor", RuntimeBinding: &auth.RuntimeBinding{SessionID: catalogSessionID, CreatorID: "owner"}}},
		{name: "execution", principal: auth.Principal{ID: "owner", Execution: &apiv1.SessionAuthority{SessionId: catalogSessionID}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &serviceTestStore{getResult: &apiv1.Session{Id: catalogSessionID, Creator: "owner"}}
			// Bound reads use the exact object grant; they do not need an empty-name collection grant.
			s := NewService(store, serviceTestAuthorizer{}, nil)
			ctx := auth.AuthSessionTo(t.Context(), catalogIdentity{principal: test.principal})
			if test.share {
				ctx = auth.ShareContextTo(ctx, &auth.ShareContext{UserID: "owner", SessionID: catalogSessionID})
			}
			result, err := s.CatalogReadScope(ctx, ownedCatalogScope())
			require.NoError(t, err)
			require.Equal(t, []string{catalogSessionID}, result.SessionIDs)
			require.Equal(t, "owner", result.OwnerID)
			require.False(t, store.unscoped)
			for _, request := range []*apiv1.TaskCatalogScope{
				{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_SessionTreeAnchor{SessionTreeAnchor: catalogRootID}},
				{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_OtherInitiators{OtherInitiators: &emptypb.Empty{}}},
			} {
				_, err = s.CatalogReadScope(ctx, request)
				require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
			}
		})
	}
}

func TestCatalogScopeCompilerIntersectsWithinClausesAndUnionsAcrossThem(t *testing.T) {
	clause := func(names ...string) authorization.ScopePredicate {
		return authorization.ScopePredicate{Attribute: authorization.AttributeName, Operator: authorization.ScopeIn, Values: names}
	}
	policy := authorization.AuthorizationScope{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{
		{All: []authorization.ScopePredicate{clause(catalogSessionID, catalogRootID), clause(catalogRootID)}},
		{All: []authorization.ScopePredicate{clause(catalogSessionID)}},
	}}
	values, err := catalogSessionNames(policy)
	require.NoError(t, err)
	require.Equal(t, []string{catalogSessionID, catalogRootID}, values)
	for _, invalid := range []authorization.AuthorizationScope{
		{Kind: "UNKNOWN"}, {Kind: authorization.ScopeAnyOf},
		{Kind: authorization.ScopeAll, AnyOf: policy.AnyOf},
		{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{}}},
		{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{All: []authorization.ScopePredicate{clause("invalid")}}}},
		{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{All: []authorization.ScopePredicate{{Attribute: authorization.AttributeNamespace, Operator: authorization.ScopeIn, Values: []string{"team-a"}}}}}},
	} {
		_, err = catalogSessionNames(invalid)
		require.Error(t, err)
	}
}
