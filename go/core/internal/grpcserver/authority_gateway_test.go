package grpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/stretchr/testify/require"
)

type gatewayProofTransport struct{ token string }

func (g gatewayProofTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("X-Substrate-Actor-Token", "Bearer "+g.token)
	return http.DefaultTransport.RoundTrip(copy)
}

// Exercise the production native callback with a real agentgateway and MCP SDK.
func authorityGatewayProof(t *testing.T, callback, token, wrongAudience, subjectToken string) func() {
	binary := os.Getenv("KAGENT_TEST_AGENTGATEWAY")
	if binary == "" {
		return func() {}
	}
	t.Helper()
	var exchanges atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid token exchange", http.StatusBadRequest)
			return
		}
		id, secret, ok := r.BasicAuth()
		if !ok || id != "destination" || secret != "destination-secret" ||
			r.Form.Get("subject_token") != subjectToken || r.Form.Get("subject_token") == token ||
			r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" || r.Form.Get("audience") != "destination-api" {
			http.Error(w, "invalid subject credential", http.StatusForbidden)
			return
		}
		exchanges.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"access_token":"destination-scoped-token","token_type":"Bearer","expires_in":600}`)
		require.NoError(t, err)
	}))
	t.Cleanup(issuer.Close)
	var calls atomic.Int32
	upstream := mcp.NewServer(&mcp.Implementation{Name: "native-authority-upstream", Version: "1"}, nil)
	for _, name := range []string{"read_nodes", "delete_nodes"} {
		upstream.AddTool(&mcp.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: r.Params.Name}}}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer destination-scoped-token" || r.Header.Get("X-Substrate-Actor-Token") != "" {
			http.Error(w, "wrong subject credential", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	// The native ext-authz check has no cache. Static MCP grants select one tool.
	config := fmt.Sprintf(`{"config":{"adminAddr":"127.0.0.1:0","readinessAddr":"127.0.0.1:0"},"binds":[{"port":%d,"listeners":[{"protocol":"HTTP","routes":[{"name":"native-authority","matches":[{"path":{"pathPrefix":"/mcp"}}],"policies":{"extAuthz":{"host":%q,"failureMode":"deny","includeRequestHeaders":["x-substrate-actor-token"],"protocol":{"http":{"path":"'/authorize/runtime?scope=write&server=team-a/native&credential=subject_token'","addRequestHeaders":{"authorization":"request.headers['x-substrate-actor-token']"},"includeResponseHeaders":["authorization"],"metadata":{"selection":"json(response.body)","scopes":"json(response.headers['x-kagent-scopes'])"}}}}},"backends":[{"mcp":{"prefixMode":"never","statefulMode":"stateless","targets":[{"name":"native","mcp":{"host":%q},"policies":{"backendAuth":{"oauthTokenExchange":{"host":%q,"path":"/token","clientAuth":{"clientId":"destination","clientSecret":"destination-secret","method":"clientSecretBasic"},"audiences":["destination-api"]}}}}]},"policies":{"mcpAuthorization":{"rules":["'write' in extauthz.scopes && (!has(extauthz.selection.tools) || mcp.tool.name in extauthz.selection.tools)"]}}}]}]}]}]}`, port, strings.TrimPrefix(callback, "http://"), server.URL+"/mcp", strings.TrimPrefix(issuer.URL, "http://"))
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("X-Substrate-Actor-Token"), "Actor proof reached model provider")
		modelCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"language"}`)
	}))
	t.Cleanup(model.Close)
	var document map[string]any
	require.NoError(t, json.Unmarshal([]byte(config), &document))
	listenerConfig := document["binds"].([]any)[0].(map[string]any)["listeners"].([]any)[0].(map[string]any)
	routes := listenerConfig["routes"].([]any)
	mcpRoute := routes[0].(map[string]any)
	mcpPolicies := mcpRoute["backends"].([]any)[0].(map[string]any)["policies"].(map[string]any)
	mcpPolicies["requestHeaderModifier"] = map[string]any{"remove": []string{"x-substrate-actor-token"}}
	for _, name := range []string{"language", "other"} {
		routes = append(routes, map[string]any{
			"name": name, "matches": []any{map[string]any{"path": map[string]any{"pathPrefix": "/v1/" + name}}},
			"policies": map[string]any{"extAuthz": map[string]any{
				"host": strings.TrimPrefix(callback, "http://"), "failureMode": "deny", "includeRequestHeaders": []string{"x-substrate-actor-token"},
				"protocol": map[string]any{"http": map[string]any{"path": "'/authorize/runtime'", "addRequestHeaders": map[string]string{"authorization": "request.headers['x-substrate-actor-token']"}, "metadata": map[string]string{"models": "json(response.body)"}}},
			}},
			"backends": []any{map[string]any{"host": strings.TrimPrefix(model.URL, "http://"), "policies": map[string]any{
				"authorization":         map[string]any{"rules": []string{"!('x-substrate-actor-token' in request.headers) || extauthz.models.exists(m, m.name == '" + name + "')"}},
				"requestHeaderModifier": map[string]any{"remove": []string{"x-substrate-actor-token"}},
			}}},
		})
	}
	listenerConfig["routes"] = routes
	encoded, err := json.Marshal(document)
	require.NoError(t, err)
	config = string(encoded)
	directory := t.TempDir()
	path := filepath.Join(directory, "gateway.json")
	require.NoError(t, os.WriteFile(path, []byte(config), 0600))
	log, err := os.Create(filepath.Join(directory, "gateway.log"))
	require.NoError(t, err)
	command := exec.Command(binary, "-f", path)
	command.Stdout, command.Stderr = log, log
	command.Env = append(os.Environ(), "RUST_LOG=warn")
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(directory, "gateway.log"))
			t.Logf("gateway: %s", data)
		}
	})
	address := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native gateway did not listen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	connect := func(raw string) (*mcp.ClientSession, error) {
		return mcp.NewClient(&mcp.Implementation{Name: "native-authority-proof", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: address, HTTPClient: &http.Client{Transport: gatewayProofTransport{raw}}}, nil)
	}
	for _, raw := range []string{"forged-token", wrongAudience} {
		session, err := connect(raw)
		if session != nil {
			_ = session.Close()
		}
		require.Error(t, err, "gateway must reject invalid or control-plane-only Actor token")
	}
	client, err := connect(token)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	tools, err := client.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, tools.Tools, 1)
	require.Equal(t, "read_nodes", tools.Tools[0].Name)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_nodes", Arguments: struct{}{}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, int32(1), calls.Load())
	for _, name := range []string{"delete_nodes", "unknown"} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: struct{}{}})
		require.True(t, err != nil || result.IsError, "gateway admitted unselected tool %s", name)
	}
	require.Equal(t, int32(1), calls.Load(), "forbidden tools must never reach upstream")
	require.Positive(t, exchanges.Load(), "RFC 8693 exchange was not used")
	modelRequest := func(name, proof string, want int) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/%s", port, name), strings.NewReader(`{"model":"language"}`))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Substrate-Actor-Token", "Bearer "+proof)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, response.Body)
		require.NoError(t, response.Body.Close())
		require.Equal(t, want, response.StatusCode, "model target %s", name)
	}
	modelRequest("language", token, http.StatusOK)
	modelRequest("other", token, http.StatusForbidden)
	modelRequest("language", wrongAudience, http.StatusUnauthorized)
	modelRequest("language", "forged-token", http.StatusUnauthorized)
	require.Equal(t, int32(1), modelCalls.Load(), "unselected model reached provider")
	// The same already-initialized MCP session must lose authority immediately.
	return func() {
		modelRequest("language", token, http.StatusForbidden)
		require.Equal(t, int32(1), modelCalls.Load(), "revoked Actor reached model provider")
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_nodes", Arguments: struct{}{}})
		require.True(t, err != nil || result.IsError, "existing MCP session survived native authority revocation")
		require.Equal(t, int32(1), calls.Load(), "revoked tool reached upstream")
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, address, strings.NewReader(`{"jsonrpc":"2.0","id":191,"method":"tools/call","params":{"name":"read_nodes","arguments":{}}}`))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("X-Substrate-Actor-Token", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, response.Body)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusForbidden, response.StatusCode)
		t.Log("PASS: real agentgateway -> native pinned model/MCP selection with stripped Actor headers, live IdP subject token exchange, Actor audience isolation, and revocation of an existing SDK session")
	}
}
