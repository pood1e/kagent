package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func TestProjectedDiscoveryCredential(t *testing.T) {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "discovery", Version: "1"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "read"}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
	var token atomic.Value
	var hits atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token.Load().(string) {
			t.Errorf("discovery authorization = %q, want current projected token", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/mcp/catalog" {
			t.Errorf("discovery path = %q", r.URL.Path)
		}
		hits.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv("KAGENT_MCP_DISCOVERY_URL", httpServer.URL)
	t.Setenv("KAGENT_MCP_DISCOVERY_TOKEN_FILE", path)
	remote := &v1alpha3.RemoteMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "team", Annotations: map[string]string{"agentos.dev/discovery-auth": "kubernetes"}},
		Spec:       v1alpha3.RemoteMCPServerSpec{URL: "http://service-scoped.invalid:8080/mcp/catalog", Protocol: v1alpha3.RemoteMCPServerProtocolStreamableHttp},
	}
	client := NewRuntimeMCPClient(toolTestKube(t, remote))
	for _, value := range []string{"first-bound-token", "rotated-bound-token"} {
		token.Store(value)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		tools, err := client.ListTools(t.Context(), MCPServerRef{Ref: types.NamespacedName{Namespace: "team", Name: "remote"}})
		if err != nil || len(tools) != 1 || tools[0].Name != "read" {
			t.Fatalf("discovery = %v, %v", tools, err)
		}
	}
	if hits.Load() < 2 {
		t.Fatal("MCP SDK did not complete discovery")
	}
}

func TestDiscoveryCredentialOrigin(t *testing.T) {
	t.Setenv("KAGENT_MCP_DISCOVERY_URL", "http://platform:8080")
	base := newMCPAppsHTTPClient(nil)
	remote := &v1alpha3.RemoteMCPServer{Spec: v1alpha3.RemoteMCPServerSpec{URL: "https://external.example/mcp"}}
	if got, err := discoveryHTTPClient(remote, base); err != nil || got != base {
		t.Fatal("external MCP authentication was changed")
	}
	remote.Annotations = map[string]string{"agentos.dev/discovery-auth": "kubernetes"}
	for _, target := range []string{"https://external.example/mcp", "http://platform:8080/untrusted", "http://platform:8080@external.example/mcp/catalog", "file:///mcp/catalog"} {
		remote.Spec.URL = target
		if _, err := discoveryHTTPClient(remote, base); err == nil {
			t.Errorf("accepted credential target %q", target)
		}
	}
}

func TestDiscoveryCredentialCannotLeaveGateway(t *testing.T) {
	var externalHits atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		externalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer external.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bound-token" {
			t.Error("gateway did not receive projected credential")
		}
		http.Redirect(w, r, external.URL+"/mcp/leak", http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("bound-token"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KAGENT_MCP_DISCOVERY_URL", gateway.URL)
	t.Setenv("KAGENT_MCP_DISCOVERY_TOKEN_FILE", path)
	remote := &v1alpha3.RemoteMCPServer{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"agentos.dev/discovery-auth": "kubernetes"}},
		Spec:       v1alpha3.RemoteMCPServerSpec{URL: external.URL + "/mcp/catalog"},
	}
	client, err := discoveryHTTPClient(remote, newMCPAppsHTTPClient(nil))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(remote.Spec.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("discovery redirect status = %d", response.StatusCode)
	}
	if externalHits.Load() != 0 {
		t.Fatal("projected credential request reached an external origin")
	}
}
