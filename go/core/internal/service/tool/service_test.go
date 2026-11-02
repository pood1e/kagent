package tool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/secretmaterial"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func TestServiceCreateToolServer(t *testing.T) {
	t.Run("creates companion secret and maps duplicate", func(t *testing.T) {
		kubeClient := toolTestKube(t)
		checker := new(recordingAuthorizer)
		service := NewService(kubeClient, &fakeDiscoveryStore{}, checker, "default", nil)
		server := &v1alpha3.RemoteMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "remote", UID: "remote-uid"},
			Spec:       v1alpha3.RemoteMCPServerSpec{URL: "https://example.com/mcp"},
		}
		request := CreateToolServerRequest{
			Type:            ServerTypeRemoteMCPServer,
			RemoteMCPServer: server,
			Secrets:         []secretmaterial.Material{{Name: "remote-token", Key: "token", Value: "secret"}},
		}

		created, err := service.CreateToolServer(toolTestContext(), request)
		require.NoError(t, err)
		require.Equal(t, "default", checker.requests[0].Checks[0].Resource.GetWorkspace().Namespace)
		require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_SERVER_MANAGE}, checker.requests[0].Checks[0].Permissions)
		assert.Equal(t, "default", created.GetNamespace())
		secret := &corev1.Secret{}
		require.NoError(t, kubeClient.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "remote-token"}, secret))
		assert.Equal(t, []byte("secret"), secret.Data["token"])
		require.Len(t, secret.OwnerReferences, 1)
		assert.Equal(t, "RemoteMCPServer", secret.OwnerReferences[0].Kind)

		duplicate := server.DeepCopy()
		duplicate.SetResourceVersion("")
		request.RemoteMCPServer = duplicate
		_, err = service.CreateToolServer(toolTestContext(), request)
		assert.True(t, serviceerrors.IsCode(err, serviceerrors.CodeAlreadyExists), err)
	})

	t.Run("authorization precedes material validation", func(t *testing.T) {
		kubeClient := toolTestKube(t)
		authorizer := &recordingAuthorizer{allow: func(*apiv1.AuthorizationResource, apiv1.Permission) bool { return false }}
		service := NewService(kubeClient, &fakeDiscoveryStore{}, authorizer, "default", nil)
		server := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Name: "denied"}}

		_, err := service.CreateToolServer(toolTestContext(), CreateToolServerRequest{
			Type:            ServerTypeRemoteMCPServer,
			RemoteMCPServer: server,
			Secrets:         []secretmaterial.Material{{Name: "INVALID NAME", Key: "bad key"}},
		})
		assert.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied), err)
		getErr := kubeClient.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "denied"}, &v1alpha3.RemoteMCPServer{})
		assert.True(t, apierrors.IsNotFound(getErr), getErr)
	})

	t.Run("rolls back owner after companion secret conflict", func(t *testing.T) {
		preexisting := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "default"},
			Type:       corev1.SecretTypeOpaque,
		}
		kubeClient := toolTestKube(t, preexisting)
		service := NewService(kubeClient, &fakeDiscoveryStore{}, &recordingAuthorizer{}, "default", nil)
		server := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Name: "local", UID: "local-uid"}}

		_, err := service.CreateToolServer(toolTestContext(), CreateToolServerRequest{
			Type:            ServerTypeRemoteMCPServer,
			RemoteMCPServer: server,
			Secrets:         []secretmaterial.Material{{Name: "shared", Key: "token", Value: "new"}},
		})
		assert.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument), err)
		getErr := kubeClient.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "local"}, &v1alpha3.RemoteMCPServer{})
		assert.True(t, apierrors.IsNotFound(getErr), getErr)
	})
}

func TestDeleteChecksManageAndFencesUID(t *testing.T) {
	ref := types.NamespacedName{Namespace: "team", Name: "remote"}
	remote := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Namespace: ref.Namespace, Name: ref.Name, UID: "remote-uid"}}
	local := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ref.Namespace, Name: ref.Name}}
	kube := toolTestKube(t, remote, local)
	checker := &recordingAuthorizer{uid: "previous-uid"}
	service := NewService(kube, &fakeDiscoveryStore{}, checker, "team", nil)
	err := service.DeleteToolServer(toolTestContext(), ref)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied), "%v", err)
	require.NoError(t, kube.Get(t.Context(), ref, &v1alpha3.RemoteMCPServer{}))
	checker.uid = "remote-uid"
	require.NoError(t, service.DeleteToolServer(toolTestContext(), ref))
	require.True(t, apierrors.IsNotFound(kube.Get(t.Context(), ref, &v1alpha3.RemoteMCPServer{})))
	require.NoError(t, kube.Get(t.Context(), ref, &corev1.Service{}))
	check := checker.requests[0].Checks[0]
	require.Equal(t, []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_SERVER_MANAGE}, check.Permissions)
	require.Equal(t, "remote", check.Resource.GetKubernetes().Ref.Name)
}
