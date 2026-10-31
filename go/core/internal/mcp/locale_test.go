package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

type languageTransport struct {
	http.RoundTripper
	locale string
}

func (r languageTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Accept-Language", r.locale)
	return r.RoundTripper.RoundTrip(request)
}

func TestMCPInvocationInheritsRequestLocale(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		t.Run(locale, func(t *testing.T) {
			gateway := &fakeGateway{completeOnDrain: true}
			handler, err := New(testSessionService(), testCheckpointService(), &a2asrv.InterceptedHandler{Handler: gateway}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r.WithContext(authContext()))
			}))
			defer server.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "locale-test", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
				Endpoint:   server.URL,
				HTTPClient: &http.Client{Transport: languageTransport{RoundTripper: http.DefaultTransport, locale: locale}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := session.Close(); err != nil {
					t.Error(err)
				}
			}()
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: invokeToolName,
				Arguments: map[string]any{"session_id": testSessionID, "message": "inspect", "background": true, "message_id": "locale-invoke"}})
			if err != nil || result.IsError {
				t.Fatalf("invoke_session = %#v, %v, content %v", result, err, result.Content)
			}
			gateway.mu.Lock()
			defer gateway.mu.Unlock()
			tag, err := apia2a.TaskResponseLocale(gateway.task)
			if err != nil || tag.String() != locale {
				t.Fatalf("delegated locale = %q, %v; want %q", tag, err, locale)
			}
		})
	}
}
