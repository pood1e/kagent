package authorization

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	fga "github.com/openfga/go-sdk"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func TestListWorkspacesFiltersAndPagesByCurrentUID(t *testing.T) {
	user, err := principalSubject("alice")
	require.NoError(t, err)
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/stores/"+testStoreID+"/batch-check", request.URL.Path)
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, testModelID, body.GetAuthorizationModelId())
		require.Equal(t, fga.CONSISTENCYPREFERENCE_HIGHER_CONSISTENCY, body.GetConsistency())
		result := make(map[string]any, len(body.Checks))
		for _, check := range body.Checks {
			require.Equal(t, user, check.TupleKey.User)
			require.Equal(t, "member", check.TupleKey.Relation)
			allowed := check.TupleKey.Object == "workspace:workspace-1" || check.TupleKey.Object == "workspace:workspace-3"
			result[check.CorrelationId] = map[string]bool{"allowed": allowed}
		}
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"result": result}))
	})
	addWorkspace(t, service, "dev", "workspace-2")
	addWorkspace(t, service, "ops", "workspace-3")
	ctx := userContext(t, "alice")
	first, err := service.ListWorkspaces(ctx, &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{Limit: 1}})
	require.NoError(t, err)
	require.Equal(t, "ops", first.Workspaces[0].Namespace)
	require.Equal(t, "workspace-3", first.Workspaces[0].Uid)
	require.NotEmpty(t, first.Page.NextPageToken)
	second, err := service.ListWorkspaces(ctx, &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{Limit: 1, PageToken: first.Page.NextPageToken}})
	require.NoError(t, err)
	require.Len(t, second.Workspaces, 1)
	require.Equal(t, "team", second.Workspaces[0].Namespace)
	require.Equal(t, "workspace-1", second.Workspaces[0].Uid)
	require.Empty(t, second.Page.NextPageToken)
	_, err = service.ListWorkspaces(userContext(t, "bob"), &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{PageToken: first.Page.NextPageToken}})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), "%v", err)
	_, err = service.ListWorkspaces(ctx, &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{PageToken: "broken"}})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), "%v", err)
}

func TestListWorkspacesUsesVerifiedGroupsAndObservedNamespaces(t *testing.T) {
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Len(t, body.Checks, 1)
		check := body.Checks[0]
		require.Equal(t, "workspace:workspace-1", check.TupleKey.Object)
		require.Equal(t, "member", check.TupleKey.Relation)
		require.Len(t, check.GetContextualTuples().TupleKeys, 1)
		require.Equal(t, "member", check.GetContextualTuples().TupleKeys[0].Relation)
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
			"result": map[string]any{check.CorrelationId: map[string]bool{"allowed": true}},
		}))
	})
	service.watchNamespaces = []string{"missing", "team", "team"}
	principal := auth.Principal{ID: "alice", Claims: map[string]any{"iss": testIssuer, "groups": []string{"ops"}}}
	ctx := auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(principal, nil))
	response, err := service.ListWorkspaces(ctx, &apiv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Equal(t, []*apiv1.WorkspaceAuthorizationReference{{Namespace: "team", Uid: "workspace-1"}}, response.Workspaces)
	require.Empty(t, response.Page.NextPageToken)
}

func TestListWorkspacesRejectsStaleMembershipAndBackendFailures(t *testing.T) {
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		result := make(map[string]any, len(body.Checks))
		for _, check := range body.Checks {
			result[check.CorrelationId] = map[string]bool{"allowed": check.TupleKey.Object == "workspace:old-uid"}
		}
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"result": result}))
	})
	response, err := service.ListWorkspaces(userContext(t, "alice"), &apiv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Empty(t, response.Workspaces)
	_, err = service.ListWorkspaces(userContext(t, "alice"), &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{Limit: 101}})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), "%v", err)

	unavailable, _ := testService(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusServiceUnavailable) })
	response, err = unavailable.ListWorkspaces(userContext(t, "alice"), &apiv1.ListWorkspacesRequest{})
	require.Nil(t, response)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable), "%v", err)
}

func TestListWorkspacesRejectsBoundAndSharedIdentities(t *testing.T) {
	service, _ := testService(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected FGA call") })
	for _, principal := range []auth.Principal{
		{ID: "alice", RuntimeBinding: &auth.RuntimeBinding{SessionID: testSessionID}},
		{ID: "alice", Execution: &apiv1.SessionAuthority{SessionId: testSessionID}},
	} {
		ctx := auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(principal, nil))
		_, err := service.ListWorkspaces(ctx, &apiv1.ListWorkspacesRequest{})
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied), "%v", err)
	}
	shared := auth.ShareContextTo(userContext(t, "alice"), &auth.ShareContext{})
	_, err := service.ListWorkspaces(shared, &apiv1.ListWorkspacesRequest{})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied), "%v", err)
}

func TestListWorkspacesContinuesAfterEmptyBoundedPage(t *testing.T) {
	var calls atomic.Int32
	service, _ := testService(t, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var body fga.BatchCheckRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		result := make(map[string]any, len(body.Checks))
		for _, check := range body.Checks {
			result[check.CorrelationId] = map[string]bool{"allowed": false}
		}
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"result": result}))
	})
	for i := range workspaceScanLimit {
		addWorkspace(t, service, fmt.Sprintf("ns-%04d", i), fmt.Sprintf("uid-%04d", i))
	}
	first, err := service.ListWorkspaces(userContext(t, "alice"), &apiv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Empty(t, first.Workspaces)
	require.NotEmpty(t, first.Page.NextPageToken)
	require.Equal(t, int32(20), calls.Load())
	second, err := service.ListWorkspaces(userContext(t, "alice"), &apiv1.ListWorkspacesRequest{Page: &apiv1.PageRequest{PageToken: first.Page.NextPageToken}})
	require.NoError(t, err)
	require.Empty(t, second.Workspaces)
	require.Empty(t, second.Page.NextPageToken)
	require.Equal(t, int32(21), calls.Load())
}

func addWorkspace(t *testing.T, service *Service, name, uid string) {
	t.Helper()
	kube := service.resolver.kube.(client.Client)
	require.NoError(t, kube.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)}}))
}
