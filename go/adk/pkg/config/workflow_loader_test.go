package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
)

func TestLoadAgentConfigsWithoutRootModel(t *testing.T) {
	for name, node := range map[string]string{
		"native agent": `{"id":"child","type":"agent","agent_ref":{"namespace":"default","name":"child"}}`,
		"CEL":          `{"id":"child","type":"cel","output":"input"}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := []byte(`{"apiVersion":"adk.declarative/v0.2","kind":"AgentSystem","agents":[{"name":"graph","agent_class":"Workflow","entry":"child","nodes":[` + node + `]}]}`)
			digest := sha256.Sum256(document)
			want := &configv1.ADKConfig{Agent: &configv1.ADKAgentConfig{
				Workflow: &configv1.ADKWorkflowConfig{Document: document, Sha256: hex.EncodeToString(digest[:])},
			}}
			data, err := Marshal(want)
			require.NoError(t, err)
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "config.json"), data, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "agent-card.json"), []byte(`{"name":"graph","version":"1"}`), 0o600))

			got, card, err := LoadAgentConfigs(directory)
			require.NoError(t, err)
			require.True(t, proto.Equal(want, got))
			require.Equal(t, "graph", card.Name)
		})
	}
}
