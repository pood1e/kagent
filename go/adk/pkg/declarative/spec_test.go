package declarative

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpecNodesAndModels(t *testing.T) {
	spec, err := Parse([]byte(`
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: flow
    agent_class: Workflow
    entry: classify
    nodes:
      - { id: classify, type: llm, model: fast, instruction: Classify. }
      - id: inner
        type: workflow
        nodes:
          - { id: answer, type: llm, model: strong, instruction: Answer. }
        edges: [{ from: START, to: answer }]
    edges: [{ from: classify, to: inner }]
  - { name: reviewer, agent_class: LlmAgent, model: fast, instruction: Review. }
`))
	require.NoError(t, err)

	var paths []string
	for path, n := range spec.Nodes() {
		paths = append(paths, path+"="+n.ID)
	}
	require.Equal(t, []string{"agents[0].nodes[0]=classify", "agents[0].nodes[1]=inner", "agents[0].nodes[1].nodes[0]=answer"}, paths)
	require.Equal(t, []string{"fast", "strong"}, spec.Models())
}
