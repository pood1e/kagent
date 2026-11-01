package grpcserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/kagent-dev/kagent/go/api/authorization"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/dbtest"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	sessionsvc "github.com/kagent-dev/kagent/go/core/internal/service/session"
	"github.com/kagent-dev/kagent/go/core/internal/service/taskcatalog"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type catalogRPCPolicy struct {
	auth.NoopAuthorizer
	allowed string
}

func (p catalogRPCPolicy) Check(_ context.Context, caller auth.Principal, _ auth.Verb, resource auth.Resource) error {
	if resource.Type == "SessionAllCreators" && caller.ID != auth.PrincipalID("trusted-proxy:", "admin") && caller.ID != auth.PrincipalID("trusted-proxy:", "restricted") {
		return errors.New("admin required")
	}
	if caller.ID == auth.PrincipalID("trusted-proxy:", "restricted") && resource.Type == "Session" && resource.Name != "" && resource.Name != p.allowed {
		return errors.New("object denied")
	}
	return nil
}

func (p catalogRPCPolicy) Scope(_ context.Context, caller auth.Principal, _ auth.Verb, _ string) (authorization.AuthorizationScope, error) {
	if caller.ID == auth.PrincipalID("trusted-proxy:", "restricted") {
		return authorization.AuthorizationScope{Kind: authorization.ScopeAnyOf, AnyOf: []authorization.ScopeClause{{All: []authorization.ScopePredicate{
			{Attribute: authorization.AttributeName, Operator: authorization.ScopeIn, Values: []string{p.allowed}},
		}}}}, nil
	}
	return authorization.AuthorizationScope{Kind: authorization.ScopeAll}, nil
}

func TestTaskCatalogGeneratedClientReadsPublishedForestAfterRootDeletion(t *testing.T) {
	dsn := dbtest.StartT(t.Context(), t)
	dbtest.MigrateT(t, dsn, false)
	db, err := database.Connect(t.Context(), &database.PostgresConfig{Pool: dbtest.PoolConfig(dsn)})
	require.NoError(t, err)
	t.Cleanup(db.Close)
	store := database.NewClient(db, "public")
	createTaskStoreSession(t, store)
	alice := auth.PrincipalID("trusted-proxy:", "alice")
	root := catalogRPCSession(t, store, nil, alice)
	child := catalogRPCSession(t, store, root, alice)
	foreign := catalogRPCSession(t, store, nil, auth.PrincipalID("trusted-proxy:", "bob"))
	empty := catalogRPCSession(t, store, nil, alice)
	rootTask, version := catalogRPCTask(t, store, root.Id, nil, "parent prompt")
	childTask, _ := catalogRPCTask(t, store, child.Id, &database.DispatchOrigin{SessionID: root.Id, Background: true}, "100%_literal RCA")
	_, _ = catalogRPCTask(t, store, foreign.Id, nil, "foreign prompt")
	policy := catalogRPCPolicy{allowed: child.Id}
	sessions := sessionsvc.NewService(store, policy, nil)
	connection := newConfigurationConnection(t, nil, func(config *Config) {
		config.Authenticator = authimpl.NewProxyAuthenticator("sub")
		config.SessionService = sessions
		config.TaskCatalogService = taskcatalog.NewService(store, sessions)
	})
	client := apiv1.NewTaskCatalogServiceClient(connection)
	ctx := memoryUserContext(t, "alice")
	owned := &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_Owned{Owned: &emptypb.Empty{}}}
	first, err := client.ListTaskRecords(ctx, &apiv1.ListTaskRecordsRequest{Scope: owned, Page: &apiv1.PageRequest{Limit: 1}})
	require.NoError(t, err)
	require.EqualValues(t, 2, first.TotalSize)
	require.Len(t, first.Tasks, 1)
	require.Empty(t, first.Tasks[0].Task.History, "summary reads do not hydrate retained messages")
	require.NotEmpty(t, first.Page.NextPageToken)
	next, err := client.ListTaskRecords(ctx, &apiv1.ListTaskRecordsRequest{Scope: owned, Page: &apiv1.PageRequest{Limit: 1, PageToken: first.Page.NextPageToken}})
	require.NoError(t, err)
	require.Len(t, next.Tasks, 1)
	require.NotEqual(t, first.Tasks[0].Task.Id, next.Tasks[0].Task.Id)
	_, err = client.ListTaskRecords(memoryUserContext(t, "bob"), &apiv1.ListTaskRecordsRequest{Scope: owned, Page: &apiv1.PageRequest{PageToken: first.Page.NextPageToken}})
	assertGRPCCode(t, err, codes.InvalidArgument)
	groups, err := client.ListTaskGroups(ctx, &apiv1.ListTaskGroupsRequest{Scope: owned, Page: &apiv1.PageRequest{Limit: 1}, View: apiv1.TaskCatalogView_TASK_CATALOG_VIEW_DETAILS})
	require.NoError(t, err)
	require.EqualValues(t, 1, groups.TotalSize)
	require.Len(t, groups.Tasks, 2)
	require.NotEmpty(t, groups.Tasks[0].Task.History)
	activity, err := client.ListAgentActivity(ctx, &apiv1.ListAgentActivityRequest{Scope: owned})
	require.NoError(t, err)
	require.Len(t, activity.Agents, 1)
	require.EqualValues(t, 2, activity.Agents[0].ActiveTasks)
	conversations, err := client.ListSessionActivity(ctx, &apiv1.ListSessionActivityRequest{Scope: owned, Agent: root.Agent, Query: "100%_literal"})
	require.NoError(t, err)
	require.EqualValues(t, 1, conversations.TotalSize)
	require.Equal(t, child.Id, conversations.Sessions[0].Session.Id)
	emptyScope := &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_SessionTreeAnchor{SessionTreeAnchor: empty.Id}}
	emptyPage, err := client.ListTaskRecords(ctx, &apiv1.ListTaskRecordsRequest{Scope: emptyScope})
	require.NoError(t, err)
	require.Empty(t, emptyPage.Tasks)
	require.Len(t, emptyPage.Sessions, 1)
	require.Equal(t, empty.Id, emptyPage.Sessions[0].Id)

	rootTask.Status = a2a.TaskStatus{State: a2a.TaskStateCompleted}
	hash := sha256.Sum256([]byte("complete-parent"))
	version, err = store.UpdateSessionTask(t.Context(), root.Id, version, hash[:], rootTask, rootTask, "")
	require.NoError(t, err)
	require.NoError(t, store.SettleSessionTask(t.Context(), root.Id, string(rootTask.ID), version))
	operation, err := store.BeginSessionOperation(t.Context(), root.Id, apiv1.RuntimeOperation_RUNTIME_OPERATION_DELETE)
	require.NoError(t, err)
	executor := uuid.New()
	claimed, err := store.ClaimSessionOperation(t.Context(), root.Id, operation.ID, executor)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = store.FinishSessionOperation(t.Context(), root.Id, operation.ID, executor, "", "", "")
	require.NoError(t, err)
	_, err = apiv1.NewSessionServiceClient(connection).GetSession(ctx, &apiv1.GetSessionRequest{SessionId: root.Id})
	assertGRPCCode(t, err, codes.NotFound)
	anchor := &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_SessionTreeAnchor{SessionTreeAnchor: child.Id}}
	visible, err := client.ListTaskRecords(ctx, &apiv1.ListTaskRecordsRequest{Scope: anchor, View: apiv1.TaskCatalogView_TASK_CATALOG_VIEW_DETAILS})
	require.NoError(t, err, "a deleted parent must not prevent reading a live analyst")
	require.Len(t, visible.Tasks, 1)
	require.Equal(t, string(childTask.ID), visible.Tasks[0].Task.Id)
	require.Equal(t, root.Id, visible.Tasks[0].Execution.Root.SessionId)
	require.Equal(t, string(rootTask.ID), visible.Tasks[0].Execution.Parent.TaskId)
	require.NotEmpty(t, visible.Tasks[0].Task.History)
	for _, value := range visible.Sessions {
		require.NotEqual(t, root.Id, value.Id)
	}
	_, err = client.ListTaskRecords(memoryUserContext(t, "bob"), &apiv1.ListTaskRecordsRequest{Scope: anchor})
	assertGRPCCode(t, err, codes.NotFound)
	other := &apiv1.TaskCatalogScope{Namespace: "team-a", Visibility: &apiv1.TaskCatalogScope_OtherInitiators{OtherInitiators: &emptypb.Empty{}}}
	_, err = client.ListTaskRecords(ctx, &apiv1.ListTaskRecordsRequest{Scope: other})
	assertGRPCCode(t, err, codes.PermissionDenied)
	all, err := client.ListTaskRecords(memoryUserContext(t, "admin"), &apiv1.ListTaskRecordsRequest{Scope: other})
	require.NoError(t, err)
	require.EqualValues(t, 2, all.TotalSize)
	restricted, err := client.ListTaskRecords(memoryUserContext(t, "restricted"), &apiv1.ListTaskRecordsRequest{Scope: other})
	require.NoError(t, err)
	require.EqualValues(t, 1, restricted.TotalSize)
	require.Len(t, restricted.Tasks, 1)
	require.Equal(t, string(childTask.ID), restricted.Tasks[0].Task.Id)
	_, err = client.ListTaskRecords(ctx, &apiv1.ListTaskRecordsRequest{Scope: owned, Page: &apiv1.PageRequest{Limit: 101}})
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func catalogRPCSession(t *testing.T, store *database.Client, parent *apiv1.Session, creator string) *apiv1.Session {
	t.Helper()
	request := &apiv1.Session{Id: uuid.NewString(), Creator: creator, Agent: &apiv1.ResourceReference{Namespace: "team-a", Name: "assistant"}}
	if parent != nil {
		request.Authority = &apiv1.SessionAuthority{SessionId: request.Id, InitiatorId: creator, ParentSessionId: parent.Id, RootSessionId: parent.Id, ScopeCeiling: &apiv1.ScopeCeiling{}}
	}
	session, _, err := store.CreateSession(t.Context(), request, uuid.NewString())
	require.NoError(t, err)
	session, err = (authorityWorkflow{store: store}).Create(t.Context(), session)
	require.NoError(t, err)
	return session
}

func catalogRPCTask(t *testing.T, store *database.Client, sessionID string, origin *database.DispatchOrigin, prompt string) (*a2a.Task, int64) {
	t.Helper()
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(prompt))
	dispatch := uuid.New()
	require.NoError(t, store.ReserveSessionDispatch(t.Context(), sessionID, dispatch, message.ID, origin))
	task := &a2a.Task{ID: a2a.TaskID(uuid.NewString()), ContextID: sessionID, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}, History: []*a2a.Message{message}}
	hash := sha256.Sum256([]byte(task.ID))
	version, err := store.CreateRuntimeTask(t.Context(), sessionID, hash[:], task, dispatch.String())
	require.NoError(t, err)
	return task, version
}

func TestTaskCatalogPoliciesAreReadOnly(t *testing.T) {
	for _, method := range []string{
		apiv1.TaskCatalogService_ListTaskGroups_FullMethodName,
		apiv1.TaskCatalogService_ListTaskRecords_FullMethodName,
		apiv1.TaskCatalogService_ListSessionActivity_FullMethodName,
		apiv1.TaskCatalogService_ListAgentActivity_FullMethodName,
	} {
		require.Equal(t, auth.AccessRead, DefaultMethodPolicies()[method])
	}
}
