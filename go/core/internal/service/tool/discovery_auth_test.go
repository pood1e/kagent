package tool

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

const catalogAppResourceURI = "ui://catalog/panel"

func discoveryServer(t *testing.T, expected *atomic.Value) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "discovery", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "read"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})
	server.AddResource(&mcp.Resource{URI: catalogAppResourceURI, Name: "panel", MIMEType: mcpAppHTMLMimeType}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: catalogAppResourceURI, MIMEType: mcpAppHTMLMimeType, Text: "<html>panel</html>"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
			http.Error(w, "discovery credential required", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	return endpoint
}

func TestOAuthDiscoveryCredentialIsLiveAndControllerOnly(t *testing.T) {
	var value atomic.Value
	value.Store("first")
	endpoint := discoveryServer(t, &value)
	var exchanges atomic.Int32
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		id, secret, ok := r.BasicAuth()
		if !ok || id != "catalog" || secret != value.Load().(string) || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "catalog.read" {
			http.Error(w, "invalid client", http.StatusUnauthorized)
			return
		}
		exchanges.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, `{"access_token":%q,"expires_in":600,"token_type":"Bearer"}`, secret)
		require.NoError(t, err)
	}))
	t.Cleanup(issuer.Close)
	remote := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "team"}, Spec: v1alpha3.RemoteMCPServerSpec{
		URL: endpoint.URL + "/mcp", Description: "catalog", DiscoveryAuth: &v1alpha3.MCPDiscoveryAuth{OAuth2: &v1alpha3.MCPDiscoveryOAuth2{
			TokenEndpoint: issuer.URL + "/token", ClientID: "catalog", ClientSecretRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "catalog"}, Key: "secret"},
			CACertificateRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "trust"}, Key: "ca.crt"}, Scopes: []string{"catalog.read"},
		}},
	}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "team"}, Data: map[string][]byte{"secret": []byte("first")}}
	bundle := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "trust", Namespace: "team"}, Data: map[string]string{"ca.crt": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw}))}}
	kube := toolTestKube(t, remote, secret, bundle)
	c := NewRuntimeMCPClient(kube)
	ref := MCPServerRef{Ref: types.NamespacedName{Namespace: "team", Name: "remote"}}
	for _, credential := range []string{"first", "rotated"} {
		value.Store(credential)
		secret.Data["secret"] = []byte(credential)
		require.NoError(t, kube.Update(t.Context(), secret))
		tools, err := c.ListTools(t.Context(), ref)
		require.NoError(t, err)
		require.Len(t, tools, 1)
		require.Equal(t, "read", tools[0].Name)
		resource, err := c.ReadResource(t.Context(), ref, catalogAppResourceURI)
		require.NoError(t, err, "catalog credentials also authorize MCP App artifacts")
		require.Len(t, resource.Contents, 1)
		require.Equal(t, "<html>panel</html>", resource.Contents[0].Text)
	}
	require.Equal(t, int32(4), exchanges.Load())
	_, err := c.CallTool(t.Context(), ref, "read", struct{}{})
	require.Error(t, err, "app calls must not borrow the catalog client")
	require.Equal(t, int32(4), exchanges.Load())
	remote.Spec.ActorJWT = &v1alpha3.ActorJWT{Audiences: []string{"https://runtime.example"}}
	require.NoError(t, kube.Update(t.Context(), remote))
	_, err = c.ListTools(t.Context(), ref)
	require.NoError(t, err, "controller discovery is independent of Actor credentials")
	_, err = c.ReadResource(t.Context(), ref, catalogAppResourceURI)
	require.NoError(t, err, "catalog artifacts are independent of Actor credentials")
	_, err = c.CallTool(t.Context(), ref, "read", struct{}{})
	require.ErrorContains(t, err, "executing Actor")
	require.Equal(t, int32(6), exchanges.Load())
}

func TestServiceAccountDiscoveryCredential(t *testing.T) {
	var value atomic.Value
	value.Store("projected")
	endpoint := discoveryServer(t, &value)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, authenticationv1.AddToScheme(scheme))
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	remote := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Name: "native", Namespace: "team"}, Spec: v1alpha3.RemoteMCPServerSpec{
		URL: endpoint.URL + "/mcp", DiscoveryAuth: &v1alpha3.MCPDiscoveryAuth{ServiceAccount: &v1alpha3.MCPDiscoveryServiceAccount{ServiceAccountRef: corev1.LocalObjectReference{Name: "controller"}, Audience: "kagent-invocation"}},
	}}
	var requests atomic.Int32
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(remote).WithInterceptorFuncs(interceptor.Funcs{SubResourceCreate: func(_ context.Context, _ client.Client, resource string, obj client.Object, sub client.Object, _ ...client.SubResourceCreateOption) error {
		require.Equal(t, "token", resource)
		require.Equal(t, "team", obj.GetNamespace())
		require.Equal(t, "controller", obj.GetName())
		request := sub.(*authenticationv1.TokenRequest)
		require.Equal(t, []string{"kagent-invocation"}, request.Spec.Audiences)
		require.Equal(t, int64(600), *request.Spec.ExpirationSeconds)
		request.Status.Token = value.Load().(string)
		requests.Add(1)
		return nil
	}}).Build()
	c := NewRuntimeMCPClient(kube)
	ref := MCPServerRef{Ref: types.NamespacedName{Namespace: "team", Name: "native"}}
	for _, credential := range []string{"projected", "rotated"} {
		value.Store(credential)
		tools, err := c.ListTools(t.Context(), ref)
		require.NoError(t, err)
		require.Len(t, tools, 1)
		resource, err := c.ReadResource(t.Context(), ref, catalogAppResourceURI)
		require.NoError(t, err)
		require.Len(t, resource.Contents, 1)
		require.Equal(t, "<html>panel</html>", resource.Contents[0].Text)
	}
	require.Equal(t, int32(4), requests.Load())
	_, err := c.CallTool(t.Context(), ref, "read", struct{}{})
	require.Error(t, err, "app calls must not borrow the catalog service account")
	require.Equal(t, int32(4), requests.Load())
}

func TestDiscoveryCredentialCannotLeaveOrigin(t *testing.T) {
	var externalHits atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { externalHits.Add(1) }))
	t.Cleanup(external.Close)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(gateway.Close)
	origin, _ := url.Parse(gateway.URL)
	client := &http.Client{Transport: &discoveryCredentialTransport{origin: origin, token: "secret", base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(gateway.URL + "/mcp")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
	_, err = client.Get(external.URL + "/mcp")
	require.ErrorContains(t, err, "configured origin")
	require.Zero(t, externalHits.Load())
}
