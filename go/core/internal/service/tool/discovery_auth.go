package tool

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/transport"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/pkg/env"
)

// Managed discovery uses the operator gateway; Actor clients retain service-scoped origins.
func discoveryHTTPClient(server *v1alpha3.RemoteMCPServer, base *http.Client) (*http.Client, error) {
	if server.Annotations["agentos.dev/discovery-auth"] != "kubernetes" {
		return base, nil
	}
	origin, err := url.Parse(env.MCPDiscoveryURL.Get())
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, fmt.Errorf("MCP discovery origin is unavailable")
	}
	target, err := url.Parse(server.Spec.URL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || !strings.HasPrefix(target.Path, "/mcp/") {
		return nil, fmt.Errorf("MCP discovery target is not a managed MCP route")
	}
	tokenFile := env.MCPDiscoveryTokenFile.Get()
	if tokenFile == "" {
		return nil, fmt.Errorf("MCP discovery token file is unavailable")
	}
	roundTripper, err := transport.NewBearerAuthWithRefreshRoundTripper("", tokenFile, base.Transport)
	if err != nil {
		return nil, fmt.Errorf("load MCP discovery token: %w", err)
	}
	result := *base
	result.Transport = &discoveryGatewayTransport{origin: origin, base: roundTripper}
	result.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &result, nil
}

type discoveryGatewayTransport struct {
	origin *url.URL
	base   http.RoundTripper
}

func (t *discoveryGatewayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(request.URL.Path, "/mcp/") {
		return nil, fmt.Errorf("MCP discovery request is not a managed MCP route")
	}
	request = request.Clone(request.Context())
	request.URL.Scheme, request.URL.Host, request.URL.User = t.origin.Scheme, t.origin.Host, nil
	request.Host = t.origin.Host
	return t.base.RoundTrip(request)
}
