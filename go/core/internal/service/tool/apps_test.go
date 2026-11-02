package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func TestMCPAppPermissionAndUIDFenceThroughSDK(t *testing.T) {
	var invocations, requests atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "authorization-test", Version: "1"}, nil)
	for _, name := range []string{"search", "delete"} {
		mcp.AddTool(server, &mcp.Tool{Name: name}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			invocations.Add(1)
			return nil, struct{}{}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	remote := &v1alpha3.RemoteMCPServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "server", UID: "server-uid"},
		Spec:       v1alpha3.RemoteMCPServerSpec{URL: endpoint.URL, Protocol: v1alpha3.RemoteMCPServerProtocolStreamableHttp},
	}
	kube := toolTestKube(t, remote)
	checker := &recordingAuthorizer{allow: func(resource *apiv1.AuthorizationResource, _ apiv1.Permission) bool {
		return resource.GetMcpTool().GetName() == "search"
	}}
	service := NewService(kube, new(fakeDiscoveryStore), checker, "team", nil)
	ref := MCPServerRef{Ref: types.NamespacedName{Namespace: "team", Name: "server"}, GroupKind: remoteMCPServerGVK.GroupKind().String()}
	_, err := service.CallMCPAppTool(toolTestContext(), ref, "search", struct{}{})
	require.NoError(t, err)
	require.Equal(t, int32(1), invocations.Load())
	completedRequests := requests.Load()
	_, err = service.CallMCPAppTool(toolTestContext(), ref, "delete", struct{}{})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	require.Equal(t, completedRequests, requests.Load())
	require.NoError(t, kube.Delete(t.Context(), remote))
	replacement := remote.DeepCopy()
	replacement.ResourceVersion, replacement.UID = "", "replacement-uid"
	require.NoError(t, kube.Create(t.Context(), replacement))
	_, err = service.CallMCPAppTool(toolTestContext(), ref, "search", struct{}{})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied), "%v", err)
	require.Equal(t, completedRequests, requests.Load())
}

func TestMCPAppToolCallsRequireTheExactToolPermission(t *testing.T) {
	ref := MCPServerRef{Ref: types.NamespacedName{Namespace: "team", Name: "server"}, GroupKind: remoteMCPServerGVK.GroupKind().String()}
	client := &fakeMCPClient{call: &mcp.CallToolResult{}, tools: []MCPAppTool{{Name: "search"}}, resource: &mcp.ReadResourceResult{}}
	checker := &recordingAuthorizer{allow: func(resource *apiv1.AuthorizationResource, permission apiv1.Permission) bool {
		return permission == apiv1.Permission_PERMISSION_MCP_TOOL_CALL && resource.GetMcpTool().GetName() == "search"
	}}
	service := NewService(nil, new(fakeDiscoveryStore), checker, "team", client)
	result, err := service.CallMCPAppTool(toolTestContext(), ref, "search", map[string]any{"query": "hello"})
	require.NoError(t, err)
	require.Same(t, client.call, result)
	require.Equal(t, types.UID("server-uid"), client.ref.UID)
	for _, name := range []string{"delete", "Search"} {
		_, err := service.CallMCPAppTool(toolTestContext(), ref, name, nil)
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied), "%v", err)
	}
	require.Equal(t, 1, client.calls)
	_, err = service.ListMCPAppTools(toolTestContext(), ref)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	_, err = service.ReadMCPAppResource(toolTestContext(), ref, "ui://panel")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	checker.allow = func(_ *apiv1.AuthorizationResource, permission apiv1.Permission) bool {
		return permission == apiv1.Permission_PERMISSION_MCP_SERVER_READ
	}
	_, err = service.ListMCPAppTools(toolTestContext(), ref)
	require.NoError(t, err)
	_, err = service.ReadMCPAppResource(toolTestContext(), ref, "ui://panel")
	require.NoError(t, err)
	_, err = service.CallMCPAppTool(toolTestContext(), ref, "search", nil)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
}

func TestMCPAppAuthorizationFailuresRemainClosed(t *testing.T) {
	ref := MCPServerRef{Ref: types.NamespacedName{Namespace: "team", Name: "server"}, GroupKind: remoteMCPServerGVK.GroupKind().String()}
	for _, checker := range []*recordingAuthorizer{
		{err: serviceerrors.NewUnavailable("authorization unavailable", nil)},
		{malformed: true},
	} {
		service := NewService(nil, new(fakeDiscoveryStore), checker, "team", nil)
		_, err := service.CallMCPAppTool(toolTestContext(), ref, "search", nil)
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable), "%v", err)
	}
	service := NewService(nil, new(fakeDiscoveryStore), nil, "team", nil)
	_, err := service.CallMCPAppTool(toolTestContext(), ref, "search", nil)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
	_, err = service.ListToolServerTypes(t.Context())
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnauthenticated))
	ref.GroupKind = "Service"
	_, err = service.CallMCPAppTool(toolTestContext(), ref, "search", nil)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument))
}
