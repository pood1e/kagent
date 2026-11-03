package tool

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	pkgauth "github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type fakeDiscoveryStore struct {
	toolsErr error
	tools    []database.Tool
	servers  []database.ToolServer
	err      error
}

func (f *fakeDiscoveryStore) ListTools(context.Context) ([]database.Tool, error) {
	if f.toolsErr != nil {
		return nil, f.toolsErr
	}
	return f.tools, f.err
}

func (f *fakeDiscoveryStore) ListToolServers(context.Context) ([]database.ToolServer, error) {
	return f.servers, f.err
}

type fakeMCPClient struct {
	tools     []MCPAppTool
	call      *mcp.CallToolResult
	resource  *mcp.ReadResourceResult
	ref       MCPServerRef
	toolName  string
	arguments any
	uri       string
	err       error
	calls     int
}

func (f *fakeMCPClient) ListTools(_ context.Context, ref MCPServerRef) ([]MCPAppTool, error) {
	f.ref = ref
	return f.tools, f.err
}

func (f *fakeMCPClient) CallTool(_ context.Context, ref MCPServerRef, toolName string, arguments any) (*mcp.CallToolResult, error) {
	f.calls++
	f.ref = ref
	f.toolName = toolName
	f.arguments = arguments
	return f.call, f.err
}

func (f *fakeMCPClient) ReadResource(_ context.Context, ref MCPServerRef, uri string) (*mcp.ReadResourceResult, error) {
	f.ref = ref
	f.uri = uri
	return f.resource, f.err
}

type recordingAuthorizer struct {
	requests  []*apiv1.CheckPermissionsRequest
	allow     func(*apiv1.AuthorizationResource, apiv1.Permission) bool
	err       error
	malformed bool
	uid       string
}

func (a *recordingAuthorizer) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	a.requests = append(a.requests, proto.CloneOf(request))
	if a.err != nil {
		return nil, a.err
	}
	result := new(apiv1.CheckPermissionsResponse)
	if a.malformed {
		return result, nil
	}
	for _, check := range request.Checks {
		resource := proto.CloneOf(check.Resource)
		switch {
		case resource.GetWorkspace() != nil:
			resource.GetWorkspace().Uid = "workspace-" + request.Workspace.Namespace
		case resource.GetKubernetes() != nil:
			resource.GetKubernetes().Uid = resource.GetKubernetes().Ref.Name + "-uid"
			if a.uid != "" {
				resource.GetKubernetes().Uid = a.uid
			}
		case resource.GetMcpTool() != nil:
			resource.GetMcpTool().Server.Uid = resource.GetMcpTool().Server.Ref.Name + "-uid"
			if a.uid != "" {
				resource.GetMcpTool().Server.Uid = a.uid
			}
		}
		access := &apiv1.ResourceAccess{Resource: resource}
		for _, permission := range check.Permissions {
			if a.allow == nil || a.allow(resource, permission) {
				access.AllowedActions = append(access.AllowedActions, permission)
			}
		}
		result.Resources = append(result.Resources, access)
	}
	return result, nil
}

func toolTestContext() context.Context {
	return pkgauth.AuthSessionTo(context.Background(), &authimpl.SimpleSession{
		P: pkgauth.Principal{ID: "tool-user"},
	})
}

func toolTestKube(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
