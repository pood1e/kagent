package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	kagentclient "github.com/kagent-dev/kagent/go/api/clientset/versioned"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func TestDiscoveryCredentialAPIRoundTrip(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../helm/kagent-crds/templates"}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	client, err := kagentclient.NewForConfig(config)
	require.NoError(t, err)
	agent := &v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Name: "model-selection"}, Spec: v1alpha3.AgentSpec{Template: v1alpha3.AgentTemplateSource{Ref: &corev1.LocalObjectReference{Name: "behavior"}}, HarnessRef: corev1.LocalObjectReference{Name: "runtime"}, ModelConfigRef: &corev1.LocalObjectReference{Name: "selected-model"}}}
	createdAgent, err := client.ApiV1alpha3().Agents("default").Create(t.Context(), agent, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Equal(t, agent.Spec.ModelConfigRef, createdAgent.Spec.ModelConfigRef)
	agent.Name, agent.ResourceVersion, agent.Spec.ModelConfigRef = "empty-model", "", &corev1.LocalObjectReference{}
	_, err = client.ApiV1alpha3().Agents("default").Create(t.Context(), agent, metav1.CreateOptions{})
	require.Error(t, err)
	servers := client.ApiV1alpha3().RemoteMCPServers("default")
	oauth := &v1alpha3.MCPDiscoveryOAuth2{TokenEndpoint: "https://identity.example/token", ClientID: "catalog", ClientSecretRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "catalog"}, Key: "client-secret"}, CACertificateRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "catalog-ca"}, Key: "ca.crt"}, Scopes: []string{"catalog"}}
	sa := &v1alpha3.MCPDiscoveryServiceAccount{ServiceAccountRef: corev1.LocalObjectReference{Name: "discovery"}, Audience: "kagent-invocation"}
	for _, test := range []struct {
		name  string
		auth  *v1alpha3.MCPDiscoveryAuth
		valid bool
	}{
		{"oauth", &v1alpha3.MCPDiscoveryAuth{OAuth2: oauth}, true},
		{"kubernetes", &v1alpha3.MCPDiscoveryAuth{ServiceAccount: sa}, true},
		{"empty", &v1alpha3.MCPDiscoveryAuth{}, false},
		{"ambiguous", &v1alpha3.MCPDiscoveryAuth{OAuth2: oauth, ServiceAccount: sa}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &v1alpha3.RemoteMCPServer{ObjectMeta: metav1.ObjectMeta{Name: test.name}, Spec: v1alpha3.RemoteMCPServerSpec{URL: "https://tools.example/mcp", Protocol: v1alpha3.RemoteMCPServerProtocolStreamableHttp, ActorJWT: &v1alpha3.ActorJWT{Audiences: []string{"https://gateway.example"}}, DiscoveryAuth: test.auth}}
			created, err := servers.Create(t.Context(), server, metav1.CreateOptions{})
			if !test.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			read, err := servers.Get(t.Context(), created.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, test.auth, read.Spec.DiscoveryAuth)
			require.Equal(t, server.Spec.ActorJWT.Audiences, read.Spec.ActorJWT.Audiences)
			require.Equal(t, int64(900), *read.Spec.ActorJWT.ExpirationSeconds)
		})
	}
}
