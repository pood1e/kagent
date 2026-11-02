package authorization

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBindingCursorIsBoundToPrincipalScopeAndModel(t *testing.T) {
	valid := bindingCursor{Principal: "alice", Object: "agent:workspace-1/agent-1", Model: testModelID, Token: "next"}
	data, err := json.Marshal(valid)
	require.NoError(t, err)
	raw := base64.RawURLEncoding.EncodeToString(data)
	token, err := decodeBindingCursor(raw, valid)
	require.NoError(t, err)
	require.Equal(t, "next", token)
	for _, test := range []struct {
		name   string
		change func(*bindingCursor)
	}{
		{"principal", func(c *bindingCursor) { c.Principal = "bob" }},
		{"scope", func(c *bindingCursor) { c.Object = "agent:workspace-2/agent-1" }},
		{"model", func(c *bindingCursor) { c.Model = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			expected := valid
			test.change(&expected)
			_, err := decodeBindingCursor(raw, expected)
			require.Error(t, err)
		})
	}
	for _, malformed := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte("{}"))} {
		_, err := decodeBindingCursor(malformed, valid)
		require.Error(t, err)
	}
}
