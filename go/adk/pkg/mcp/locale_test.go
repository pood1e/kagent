package mcp

import (
	"net/http"
	"testing"

	"golang.org/x/text/language"

	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
)

func TestMCPResponseLocaleUsesActiveCallOnly(t *testing.T) {
	for _, tag := range []language.Tag{language.English, language.Chinese} {
		ctx, end := startCallScope(apia2a.WithResponseLocale(t.Context(), tag))
		base := &recordingTransport{}
		transport := &headerRoundTripper{base: base}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mcp.example/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if got := base.header.Get("Accept-Language"); got != tag.String() {
			t.Fatalf("MCP response locale = %q, want %q", got, tag)
		}
		end()
		response, err = transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if got := base.header.Get("Accept-Language"); got != "" {
			t.Fatalf("completed call leaked its locale into the shared MCP session: %q", got)
		}
	}
}
