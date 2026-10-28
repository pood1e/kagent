package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type authorityExchangeGateway struct {
	address    string
	calls      atomic.Int32
	generation atomic.Value
}

func startAuthorityExchangeGateway(t *testing.T, f *authorityOAuthIssuer, callback string, port int) *authorityExchangeGateway {
	t.Helper()
	gateway := &authorityExchangeGateway{address: fmt.Sprintf("http://127.0.0.1:%d/mcp", port)}
	gateway.generation.Store("")
	upstream := mcp.NewServer(&mcp.Implementation{Name: "native-oauth-upstream", Version: "1"}, nil)
	for _, name := range []string{"read_nodes", "delete_nodes"} {
		upstream.AddTool(&mcp.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			gateway.calls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: r.Params.Name}}}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if !strings.HasPrefix(token, "Bearer environment-alice-renewed-") || r.Header.Get("X-Substrate-Actor-Token") != "" || r.Header.Get("Cookie") != "" {
			http.Error(w, "environment credential isolation required", http.StatusForbidden)
			return
		}
		gateway.generation.Store(strings.TrimPrefix(token, "Bearer environment-alice-"))
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	config := fmt.Sprintf(`{
 "config":{"adminAddr":"127.0.0.1:0","readinessAddr":"127.0.0.1:0"},
 "binds":[{"port":%d,"listeners":[{"protocol":"HTTP","routes":[{
  "name":"native-oauth-authority","matches":[{"path":{"pathPrefix":"/mcp"}}],
  "policies":{"extAuthz":{
   "host":%q,"failureMode":"deny","includeRequestHeaders":["x-substrate-actor-token"],
   "protocol":{"http":{
    "path":"'/authorize/runtime?scope=read&server=team-a/environment&credential=subject_token'",
    "addRequestHeaders":{"authorization":"request.headers['x-substrate-actor-token']"},
    "includeResponseHeaders":["authorization"],"metadata":{"selection":"json(response.body)","scopes":"json(response.headers['x-kagent-scopes'])"}
   }}
  }},
  "backends":[{
   "mcp":{"prefixMode":"never","statefulMode":"stateless","targets":[{
    "name":"environment","mcp":{"host":%q},
    "policies":{
     "requestHeaderModifier":{"remove":["x-substrate-actor-token","cookie","x-forwarded-access-token"]},
     "backendAuth":{"oauthTokenExchange":{
      "host":%q,"path":"/token","policies":{"backendTLS":{"root":%q}},
      "clientAuth":{"clientId":"environment-client","clientSecret":"environment-client-secret","method":"clientSecretBasic"},
      "audiences":[%q],"scopes":["environment-read"],"requestedTokenType":%q
     }}
    }
   }]},
   "policies":{"mcpAuthorization":{"rules":["(!has(extauthz.selection.tools) || mcp.tool.name in extauthz.selection.tools) && (!mcp.tool.name.startsWith('delete_') || 'write' in extauthz.scopes)"]}}
  }]
 }]}]}]
 }`, port, strings.TrimPrefix(callback, "http://"), server.URL+"/mcp", strings.TrimPrefix(f.endpoint.URL, "https://"), f.ca, exchangeAudience, accessTokenType)
	directory := t.TempDir()
	path := filepath.Join(directory, "gateway.json")
	require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
	log, err := os.Create(filepath.Join(directory, "gateway.log"))
	require.NoError(t, err)
	command := exec.Command(os.Getenv("KAGENT_TEST_AGENTGATEWAY"), "-f", path)
	command.Stdout, command.Stderr = log, log
	command.Env = append(os.Environ(), "RUST_LOG=warn")
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("stop gateway: %v", err)
		}
		var exitError *exec.ExitError
		if err := command.Wait(); err != nil && !errors.As(err, &exitError) {
			t.Errorf("wait for gateway: %v", err)
		}
		assert.NoError(t, log.Close())
		if t.Failed() {
			data, err := os.ReadFile(filepath.Join(directory, "gateway.log"))
			require.NoError(t, err)
			t.Logf("gateway: %s", data)
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			require.NoError(t, conn.Close())
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native OAuth gateway did not listen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return gateway
}

func (g *authorityExchangeGateway) connect(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	return mcp.NewClient(&mcp.Implementation{Name: "native-oauth-proof", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: g.address, HTTPClient: &http.Client{Transport: gatewayProofTransport{token}}}, nil)
}
