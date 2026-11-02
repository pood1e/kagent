package admission

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func TestWorkflowCandidateUsesReadOnlySnapshot(t *testing.T) {
	service, agent, _ := admissionFixture(t, "kagent")
	source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: agent.Namespace, Name: "workflow"}, Data: map[string]string{"workflow.yaml": "old", "prompt.md": "Analyze."}}
	require.NoError(t, service.kube.Create(t.Context(), source))
	agent.Spec.Template = v1alpha3.AgentTemplateSource{Inline: &v1alpha3.AgentTemplateSpec{WorkflowFrom: &v1alpha3.ConfigMapKeyReference{Name: source.Name, Key: "workflow.yaml"}}}
	document := []byte(`apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: graph
    agent_class: Workflow
    entry: done
    nodes:
      - {id: done, type: cel, output: input}
`)
	revision, err := service.Workflow(t.Context(), agent, nil, document)
	require.NoError(t, err)
	require.Len(t, revision, 64)
	stored := &corev1.ConfigMap{}
	require.NoError(t, service.kube.Get(t.Context(), client.ObjectKeyFromObject(source), stored))
	require.Equal(t, source.Data, stored.Data)
	invalid := []byte(`apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: graph
    agent_class: Workflow
    entry: a
    nodes:
      - {id: a, type: cel, output: input}
      - {id: b, type: cel, output: input}
    edges:
      - {from: a, to: b}
      - {from: b, to: a}
`)
	_, err = service.Workflow(t.Context(), agent, nil, invalid)
	require.ErrorContains(t, err, "cycle")
	require.NoError(t, service.kube.Get(t.Context(), client.ObjectKeyFromObject(source), stored))
	require.Equal(t, "old", stored.Data["workflow.yaml"])
}

func TestReferencedWorkflowRevisionMatchesPersistedCompilation(t *testing.T) {
	service, agent, _ := admissionFixture(t, "kagent")
	template := &v1alpha3.AgentTemplate{}
	require.NoError(t, service.kube.Get(t.Context(), client.ObjectKey{Namespace: agent.Namespace, Name: agent.Spec.Template.Ref.Name}, template))
	previous := template.DeepCopy()
	template.Spec.SystemPrompt = ""
	template.Spec.WorkflowFrom = &v1alpha3.ConfigMapKeyReference{Name: "candidate", Key: "workflow.yaml"}
	document := []byte(`apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: graph
    agent_class: Workflow
    entry: done
    nodes:
      - {id: done, type: cel, output: input}
`)
	candidate, err := service.Workflow(t.Context(), agent, template, document)
	require.NoError(t, err)
	stored := &v1alpha3.AgentTemplate{}
	require.NoError(t, service.kube.Get(t.Context(), client.ObjectKeyFromObject(template), stored))
	require.Equal(t, previous.Spec, stored.Spec)
	require.Error(t, service.kube.Get(t.Context(), client.ObjectKey{Namespace: agent.Namespace, Name: "candidate"}, &corev1.ConfigMap{}))
	require.NoError(t, service.kube.Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: agent.Namespace, Name: "candidate"}, Data: map[string]string{"workflow.yaml": string(document)}}))
	require.NoError(t, service.kube.Update(t.Context(), template))
	published, err := service.WorkflowRevision(t.Context(), client.ObjectKeyFromObject(agent))
	require.NoError(t, err)
	require.Equal(t, candidate, published)
	template.Namespace = "foreign"
	_, err = service.Workflow(t.Context(), agent, template, document)
	require.ErrorContains(t, err, "reference mismatch")
}
