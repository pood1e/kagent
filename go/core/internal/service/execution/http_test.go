package execution

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func TestMCPSelectionUsesExactServerAndPreservesAllTools(t *testing.T) {
	bindings := []v1alpha3.MCPToolBinding{
		{Server: corev1.TypedLocalObjectReference{Kind: "RemoteMCPServer", Name: "selected"}, Tools: []string{"read", "read"}},
		{Server: corev1.TypedLocalObjectReference{Kind: "RemoteMCPServer", Name: "other"}, Tools: []string{"delete"}},
		{Server: corev1.TypedLocalObjectReference{Kind: "RemoteMCPServer", Name: "selected"}, Tools: []string{"list"}},
	}
	selected, found := selectedMCPTools("team", bindings, "team/selected")
	require.True(t, found)
	require.Equal(t, []string{"list", "read"}, selected.Tools)
	_, found = selectedMCPTools("team", bindings, "other/selected")
	require.False(t, found)
	bindings[2].Tools = nil
	selected, found = selectedMCPTools("team", bindings, "team/selected")
	require.True(t, found)
	require.Empty(t, selected.Tools)
}
