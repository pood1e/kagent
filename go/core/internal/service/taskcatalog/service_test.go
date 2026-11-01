package taskcatalog

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type catalogTestStore struct {
	query     database.CatalogTaskQuery
	calls     int
	err       error
	records   database.CatalogRecords
	renewable bool
	checks    int
}

func (s *catalogTestStore) CatalogHasRenewableSession(context.Context, database.CatalogScope, string) (bool, error) {
	s.checks++
	return s.renewable, s.err
}

func (s *catalogTestStore) ListCatalogTaskGroups(ctx context.Context, q database.CatalogTaskQuery) (database.CatalogGroups, error) {
	s.calls++
	s.query = q
	return database.CatalogGroups{CatalogRecords: s.records}, errors.Join(s.err, ctx.Err())
}

func (s *catalogTestStore) ListCatalogTaskRecords(ctx context.Context, q database.CatalogTaskQuery) (database.CatalogRecords, error) {
	s.calls++
	s.query = q
	return s.records, errors.Join(s.err, ctx.Err())
}

func (s *catalogTestStore) ListCatalogSessionActivity(ctx context.Context, q database.CatalogTaskQuery) (database.CatalogSessionPage, error) {
	s.calls++
	s.query = q
	return database.CatalogSessionPage{Total: s.records.Total, NextID: s.records.NextID, NextTime: s.records.NextTime}, errors.Join(s.err, ctx.Err())
}

func (s *catalogTestStore) ListCatalogAgentActivity(ctx context.Context, scope database.CatalogScope) ([]*apiv1.AgentActivity, error) {
	s.calls++
	s.query.Scope = scope
	return []*apiv1.AgentActivity{{Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "rca"}, ActiveTasks: 2}}, errors.Join(s.err, ctx.Err())
}

type catalogTestSessions struct {
	scope    database.CatalogScope
	err      error
	renewals int
}

func (s *catalogTestSessions) CatalogReadScope(context.Context, *apiv1.TaskCatalogScope) (database.CatalogScope, error) {
	return s.scope, s.err
}
func (s *catalogTestSessions) RenewCatalogRead(context.Context) error { s.renewals++; return s.err }

func ownedScope() *apiv1.TaskCatalogScope {
	return &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_Owned{Owned: &emptypb.Empty{}}}
}

func TestCatalogRequestsNormalizeSelectionAndUseOneDirectoryQuery(t *testing.T) {
	store, sessions := &catalogTestStore{}, &catalogTestSessions{scope: database.CatalogScope{Namespace: "team-a", OwnerID: "alice"}}
	s := NewService(store, sessions)
	ref := &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}
	response, err := s.ListTaskGroups(t.Context(), &apiv1.ListTaskGroupsRequest{
		Scope: ownedScope(), Query: "  100%_literal  ", BoundaryAgents: []*apiv1.ResourceReference{ref, ref},
		Activities: []*apiv1.TaskActivityFilter{{State: a2apb.TaskState_TASK_STATE_UNSPECIFIED.Enum(), PendingToolApproval: new(false)}},
	})
	require.NoError(t, err)
	require.Empty(t, response.Page.NextPageToken)
	require.Equal(t, 1, store.calls)
	require.Equal(t, 50, store.query.Limit)
	require.Equal(t, "100%_literal", store.query.Query)
	require.Equal(t, []string{"team-a/assistant"}, store.query.BoundaryAgents)
	require.Equal(t, "TASK_STATE_UNSPECIFIED", *store.query.Filters[0].State)
	require.False(t, *store.query.Filters[0].PendingApproval)

	_, err = s.ListTaskRecords(t.Context(), &apiv1.ListTaskRecordsRequest{Scope: ownedScope(), Page: &apiv1.PageRequest{Limit: 3}})
	require.NoError(t, err)
	require.Equal(t, 3, store.query.Limit)
	_, err = s.ListSessionActivity(t.Context(), &apiv1.ListSessionActivityRequest{Scope: ownedScope(), Agent: ref})
	require.NoError(t, err)
	activity, err := s.ListAgentActivity(t.Context(), &apiv1.ListAgentActivityRequest{Scope: ownedScope()})
	require.NoError(t, err)
	require.EqualValues(t, 2, activity.Agents[0].ActiveTasks)
	require.Equal(t, 4, store.calls)
}

func TestCatalogValidationRejectsBeforeScopeAndStorage(t *testing.T) {
	store := &catalogTestStore{}
	s := NewService(store, &catalogTestSessions{})
	for _, request := range []*apiv1.ListTaskRecordsRequest{
		nil, {}, {Scope: &apiv1.TaskCatalogScope{Namespace: "team-a"}},
		{Scope: ownedScope(), Page: &apiv1.PageRequest{Limit: 101}},
		{Scope: ownedScope(), Activities: []*apiv1.TaskActivityFilter{{}}},
		{Scope: ownedScope(), Activities: []*apiv1.TaskActivityFilter{{State: a2apb.TaskState(999).Enum()}}},
		{Scope: ownedScope(), Agent: &apiv1.ResourceReference{Namespace: "INVALID", Name: "rca"}},
		{Scope: ownedScope(), View: apiv1.TaskCatalogView(999)},
	} {
		_, err := s.ListTaskRecords(t.Context(), request)
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), "%v", err)
	}
	_, err := s.ListSessionActivity(t.Context(), &apiv1.ListSessionActivityRequest{Scope: ownedScope()})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument))
	require.Zero(t, store.calls)
}

func TestCatalogCursorBindsCurrentScopeAndSelection(t *testing.T) {
	at := time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC)
	store := &catalogTestStore{records: database.CatalogRecords{Total: 12, NextID: "native-task", NextTime: &at}}
	sessions := &catalogTestSessions{scope: database.CatalogScope{Namespace: "team-a", OwnerID: "alice"}}
	s := NewService(store, sessions)
	first, err := s.ListTaskRecords(t.Context(), &apiv1.ListTaskRecordsRequest{Scope: ownedScope()})
	require.NoError(t, err)
	require.EqualValues(t, 12, first.TotalSize)
	token := first.Page.NextPageToken
	require.NotEmpty(t, token)
	request := &apiv1.ListTaskRecordsRequest{Scope: ownedScope(), Page: &apiv1.PageRequest{PageToken: token, Limit: 2}, View: apiv1.TaskCatalogView_TASK_CATALOG_VIEW_DETAILS}
	_, err = s.ListTaskRecords(t.Context(), request)
	require.NoError(t, err, "page size and payload hydration do not change selection")
	require.Equal(t, "native-task", store.query.AfterID)
	require.Equal(t, at, *store.query.AfterTime)
	before := store.calls
	sessions.scope.OwnerID = "bob"
	_, err = s.ListTaskRecords(t.Context(), request)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument))
	require.Equal(t, before, store.calls)
	sessions.scope.OwnerID = "alice"
	request.Agent = &apiv1.ResourceReference{Namespace: "team-a", Name: "rca"}
	_, err = s.ListTaskRecords(t.Context(), request)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument))
	_, err = s.ListTaskGroups(t.Context(), &apiv1.ListTaskGroupsRequest{Scope: ownedScope(), Page: request.Page})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), "cursors are RPC-specific")
}

type catalogPrincipal struct{ value auth.Principal }

func (p catalogPrincipal) Principal() auth.Principal { return p.value }

func TestCatalogRenewsOnlyUnboundOwnedCollections(t *testing.T) {
	for _, test := range []struct {
		name                 string
		principal            auth.Principal
		share, anchor, other bool
		want                 int
	}{
		{name: "owned", principal: auth.Principal{ID: "alice"}, want: 1},
		{name: "shared", principal: auth.Principal{ID: "alice"}, share: true},
		{name: "actor", principal: auth.Principal{ID: "alice", RuntimeBinding: &auth.RuntimeBinding{SessionID: "bound"}}},
		{name: "execution", principal: auth.Principal{ID: "alice", Execution: &apiv1.SessionAuthority{SessionId: "bound"}}},
		{name: "anchor-already-authorized", principal: auth.Principal{ID: "alice"}, anchor: true},
		{name: "other-initiators", principal: auth.Principal{ID: "alice"}, other: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &catalogTestStore{renewable: true}
			sessions := &catalogTestSessions{scope: database.CatalogScope{Namespace: "team-a", OwnerID: "alice"}}
			if test.anchor {
				sessions.scope.AnchorSessionID = "anchor"
			}
			if test.other {
				sessions.scope.OwnerID = ""
				sessions.scope.OtherInitiatorID = "alice"
			}
			ctx := auth.AuthSessionTo(t.Context(), catalogPrincipal{value: test.principal})
			if test.share {
				ctx = auth.ShareContextTo(ctx, &auth.ShareContext{SessionID: "anchor"})
			}
			_, err := NewService(store, sessions).ListAgentActivity(ctx, &apiv1.ListAgentActivityRequest{Scope: ownedScope()})
			require.NoError(t, err)
			require.Equal(t, test.want, store.checks)
			require.Equal(t, test.want, sessions.renewals)
			require.Equal(t, 1, store.calls)
		})
	}
}

func TestCatalogCancellationAndStorageErrorsPropagate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewService(&catalogTestStore{}, &catalogTestSessions{}).ListTaskRecords(ctx, &apiv1.ListTaskRecordsRequest{Scope: ownedScope()})
	require.ErrorIs(t, err, context.Canceled)
	_, _, err = pageMetadata("binding", "", nil, math.MaxInt32+1)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeResourceExhausted))
}
