package tool

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func catalogRemote(namespace, name string, tools ...string) *v1alpha3.RemoteMCPServer {
	server := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(name + "-uid")}}
	for _, tool := range tools {
		server.Status.DiscoveredTools = append(server.Status.DiscoveredTools, &v1alpha3.MCPTool{Name: tool, Description: "current " + tool})
	}
	return server
}

func TestCatalogFiltersThroughCurrentAuthorizedServer(t *testing.T) {
	remote := remoteMCPServerGVK.GroupKind().String()
	store := &fakeDiscoveryStore{servers: []database.ToolServer{
		{Name: "team/allowed", GroupKind: remote},
		{Name: "team/allowed", GroupKind: "Service"},
		{Name: "team/denied", GroupKind: remote},
		{Name: "other/allowed", GroupKind: remote},
		{Name: "team/not-discovered", GroupKind: remote},
		{Name: "team/missing", GroupKind: remote},
		{Name: "team/invalid/name", GroupKind: remote},
	}, tools: []database.Tool{
		{ID: "search", ServerName: "team/allowed", GroupKind: remote, Description: "old description"},
		{ID: "removed", ServerName: "team/allowed", GroupKind: remote},
		{ID: "search", ServerName: "team/allowed", GroupKind: "Service"},
		{ID: "private", ServerName: "team/denied", GroupKind: remote},
		{ID: "search", ServerName: "other/allowed", GroupKind: remote},
	}}
	checker := &recordingAuthorizer{allow: func(resource *apiv1.AuthorizationResource, permission apiv1.Permission) bool {
		ref := resource.GetKubernetes().Ref
		return permission == apiv1.Permission_PERMISSION_MCP_SERVER_READ && ref.Namespace == "team" && ref.Name != "denied"
	}}
	service := NewService(toolTestKube(t,
		catalogRemote("team", "allowed", "search"), catalogRemote("team", "denied", "private"),
		catalogRemote("other", "allowed", "search"), catalogRemote("team", "not-discovered"),
	), store, checker, "team", nil)
	_, err := service.ListTools(context.Background())
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnauthenticated))
	listed, err := service.ListToolServers(toolTestContext())
	require.NoError(t, err)
	require.Equal(t, []ToolServer{
		{Ref: "team/allowed", GroupKind: remote, DiscoveredTools: []*v1alpha3.MCPTool{{Name: "search", Description: "current search"}}},
		{Ref: "team/not-discovered", GroupKind: remote, DiscoveredTools: []*v1alpha3.MCPTool{}},
	}, listed)
	flat, err := service.ListTools(toolTestContext())
	require.NoError(t, err)
	require.Len(t, flat, 1)
	require.Equal(t, "current search", flat[0].Description)
	checker.uid = "prior-server"
	listed, err = service.ListToolServers(toolTestContext())
	require.NoError(t, err)
	require.Empty(t, listed)
}

func TestCatalogBatchesChecksBeforeProjection(t *testing.T) {
	store := new(fakeDiscoveryStore)
	objects := make([]client.Object, 205)
	for i := range objects {
		name := fmt.Sprintf("server-%03d", i)
		objects[i] = catalogRemote("team", name)
		store.servers = append(store.servers, database.ToolServer{Name: "team/" + name, GroupKind: remoteMCPServerGVK.GroupKind().String()})
	}
	checker := new(recordingAuthorizer)
	service := NewService(toolTestKube(t, objects...), store, checker, "team", nil)
	servers, err := service.ListToolServers(toolTestContext())
	require.NoError(t, err)
	require.Len(t, servers, 205)
	require.Len(t, checker.requests, 3)
	for i, size := range []int{100, 100, 5} {
		require.Len(t, checker.requests[i].Checks, size)
		require.Equal(t, "team", checker.requests[i].Workspace.Namespace)
	}
	checker.err = serviceerrors.NewUnavailable("authorization unavailable", nil)
	_, err = service.ListTools(toolTestContext())
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
	checker.err, checker.malformed = nil, true
	_, err = service.ListTools(toolTestContext())
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
}

func TestCatalogHidesToolsUntilCurrentGenerationIsObserved(t *testing.T) {
	server := catalogRemote("team", "remote", "search")
	server.Generation = 2
	server.Status.ObservedGeneration = 1
	store := &fakeDiscoveryStore{
		servers: []database.ToolServer{{Name: "team/remote", GroupKind: remoteMCPServerGVK.GroupKind().String()}},
		tools:   []database.Tool{{ID: "search", ServerName: "team/remote", GroupKind: remoteMCPServerGVK.GroupKind().String()}},
	}
	service := NewService(toolTestKube(t, server), store, new(recordingAuthorizer), "team", nil)
	tools, err := service.ListTools(toolTestContext())
	require.NoError(t, err)
	require.Empty(t, tools)
}
