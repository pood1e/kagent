package translator_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	runtimeconfig "github.com/kagent-dev/kagent/go/adk/pkg/config"
	"github.com/kagent-dev/kagent/go/adk/pkg/declarative"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

const nativeWorkflowYAML = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: root
    agent_class: Workflow
    entry: call
    nodes:
      - {id: call, type: agent, agent_ref: {name: analyst}}
`

func TestCompileNativeWorkflowWithoutModelOrTools(t *testing.T) {
	harness := kagentHarness()
	root := inlineAgent(harness, workflowTemplate(nil))
	child := inlineAgent(harness, &v1alpha3.AgentTemplate{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "test"}})
	child.Name = "analyst"
	result, err := compiler(t, harness, child, workflowConfigMap(nativeWorkflowYAML)).CompileAgent(t.Context(), root)
	require.NoError(t, err)
	var config configv1.ADKConfig
	require.NoError(t, runtimeconfig.Unmarshal(result.ConfigJSON, &config))
	require.Nil(t, config.Agent.Model)
	require.Empty(t, config.Agent.Workflow.Models)
	spec, err := declarative.Parse(config.Agent.Workflow.Document)
	require.NoError(t, err)
	require.Equal(t, "test", spec.Agents[0].Nodes[0].AgentRef.Namespace)
	require.Contains(t, string(result.Provenance), `"kind":"ConfigMap"`)
	// Compilation defaults a private copy, preserving informer-owned inputs.
	require.Nil(t, root.Spec.ModelConfigRef)
	harness.Spec.Kagent.Compaction = &v1alpha3.KagentHarnessCompaction{CompactionInterval: new(2)}
	_, err = compiler(t, harness, child, workflowConfigMap(nativeWorkflowYAML)).CompileAgent(t.Context(), root)
	require.ErrorContains(t, err, "compaction requires summarizer.modelConfigRef")
}

func TestCompileNativeWorkflowRejectsMissingAndCyclicReferences(t *testing.T) {
	harness := kagentHarness()
	for _, name := range []string{"missing", "self", "transitive", "referenced-template"} {
		t.Run(name, func(t *testing.T) {
			root := inlineAgent(harness, workflowTemplate(nil))
			objects := []any{harness, root}
			document := nativeWorkflowYAML
			want := `Agent "test/analyst" not found`
			switch name {
			case "self":
				document = strings.ReplaceAll(document, "name: analyst", "name: "+root.Name)
				want = "native Agent workflow cycle: test/" + root.Name + " -> test/" + root.Name
			case "transitive", "referenced-template":
				template := workflowTemplate(nil)
				template.Name = "analyst"
				template.Spec.WorkflowFrom = &v1alpha3.ConfigMapKeyReference{Name: "nested", Key: "agents.yaml"}
				child := inlineAgent(harness, template)
				child.Name = "analyst"
				if name == "referenced-template" {
					child.Spec.Template = v1alpha3.AgentTemplateSource{Ref: &corev1.LocalObjectReference{Name: template.Name}}
					objects = append(objects, template)
				}
				objects = append(objects, child, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "nested", Namespace: "test"},
					Data:       map[string]string{"agents.yaml": strings.ReplaceAll(document, "name: analyst", "name: "+root.Name)},
				})
				want = "native Agent workflow cycle: test/" + root.Name + " -> test/analyst -> test/" + root.Name
			}
			objects = append(objects, workflowConfigMap(document))
			_, err := compiler(t, objects...).CompileAgent(t.Context(), root)
			require.ErrorContains(t, err, want)
		})
	}
}
