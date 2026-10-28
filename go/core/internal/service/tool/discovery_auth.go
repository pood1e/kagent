package tool

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func (c *RuntimeMCPClient) discoveryHTTPClient(ctx context.Context, server *v1alpha3.RemoteMCPServer, base *http.Client) (*http.Client, error) {
	origin, err := url.Parse(server.Spec.URL)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Hostname() == "" || origin.User != nil {
		return nil, fmt.Errorf("discovery requires an HTTP(S) server URL")
	}
	var token string
	switch source := server.Spec.DiscoveryAuth; {
	case source.ServiceAccount != nil && source.OAuth2 == nil:
		account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: server.Namespace, Name: source.ServiceAccount.ServiceAccountRef.Name}}
		request := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: []string{source.ServiceAccount.Audience}, ExpirationSeconds: new(int64(600))}}
		if err := c.kubeClient.SubResource("token").Create(ctx, account, request); err != nil {
			return nil, fmt.Errorf("request discovery service-account token: %w", err)
		}
		token = request.Status.Token
	case source.OAuth2 != nil && source.ServiceAccount == nil:
		token, err = c.discoveryOAuthToken(ctx, server.Namespace, source.OAuth2)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("discovery requires exactly one credential source")
	}
	if token == "" {
		return nil, fmt.Errorf("discovery credential is empty")
	}
	result := *base
	result.Transport = &discoveryCredentialTransport{origin: origin, token: token, base: base.Transport}
	result.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &result, nil
}

func (c *RuntimeMCPClient) discoveryOAuthToken(ctx context.Context, namespace string, source *v1alpha3.MCPDiscoveryOAuth2) (string, error) {
	u, err := url.Parse(source.TokenEndpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("discovery OAuth token endpoint requires HTTPS")
	}
	secret := &corev1.Secret{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.ClientSecretRef.Name}, secret); err != nil {
		return "", fmt.Errorf("get discovery OAuth client Secret: %w", err)
	}
	value := secret.Data[source.ClientSecretRef.Key]
	if len(value) == 0 {
		return "", fmt.Errorf("discovery OAuth client Secret key is empty")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	if source.CACertificateRef != nil {
		bundle := &corev1.ConfigMap{}
		if err := c.kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.CACertificateRef.Name}, bundle); err != nil {
			return "", fmt.Errorf("get discovery OAuth CA bundle: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return "", fmt.Errorf("load discovery OAuth system trust: %w", err)
		}
		if !roots.AppendCertsFromPEM([]byte(bundle.Data[source.CACertificateRef.Key])) {
			return "", fmt.Errorf("discovery OAuth CA bundle has no certificates")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	config := clientcredentials.Config{ClientID: source.ClientID, ClientSecret: string(value), TokenURL: source.TokenEndpoint, Scopes: source.Scopes, AuthStyle: oauth2.AuthStyleInHeader}
	credential, err := config.Token(context.WithValue(ctx, oauth2.HTTPClient, httpClient))
	if err != nil {
		return "", fmt.Errorf("get discovery OAuth credential: %w", err)
	}
	return credential.AccessToken, nil
}

type discoveryCredentialTransport struct {
	origin *url.URL
	token  string
	base   http.RoundTripper
}

func (t *discoveryCredentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != t.origin.Scheme || request.URL.Host != t.origin.Host {
		return nil, fmt.Errorf("discovery credential cannot leave the configured origin")
	}
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(request)
}
