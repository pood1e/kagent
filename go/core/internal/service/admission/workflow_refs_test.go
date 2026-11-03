package admission

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func nativeReferenceDocument(name string) []byte {
	return fmt.Appendf(nil, `apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: graph
    agent_class: Workflow
    entry: delegate
    nodes:
      - {id: delegate, type: agent, agent_ref: {name: %s}}
`, name)
}

func TestWorkflowAdmissionResolvesNativeAgentReferences(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%t", inline), func(t *testing.T) {
			service, agent, _ := admissionFixture(t, "kagent")
			target := agent.DeepCopy()
			target.Name, target.ResourceVersion = "developer", ""
			if inline {
				template := &v1alpha3.AgentTemplate{}
				require.NoError(t, service.kube.Get(t.Context(), client.ObjectKey{Namespace: agent.Namespace, Name: agent.Spec.Template.Ref.Name}, template))
				target.Spec.Template = v1alpha3.AgentTemplateSource{Inline: template.Spec.DeepCopy()}
			}
			require.NoError(t, service.kube.Create(t.Context(), target))
			unrelated := target.DeepCopy()
			unrelated.Name, unrelated.ResourceVersion, unrelated.Spec.HarnessRef.Name = "unrelated", "", "missing"
			require.NoError(t, service.kube.Create(t.Context(), unrelated))
			agent.Spec.Template = v1alpha3.AgentTemplateSource{Inline: &v1alpha3.AgentTemplateSpec{WorkflowFrom: &v1alpha3.ConfigMapKeyReference{Name: "workflow", Key: "workflow.yaml"}}}
			document := nativeReferenceDocument(target.Name)
			candidate, err := service.Workflow(t.Context(), agent, nil, document)
			require.NoError(t, err)
			require.Len(t, candidate, 64)
			stored := &v1alpha3.Agent{}
			require.NoError(t, service.kube.Get(t.Context(), client.ObjectKeyFromObject(agent), stored))
			require.NotNil(t, stored.Spec.Template.Ref, "candidate must remain read-only")
			require.NoError(t, service.kube.Update(t.Context(), agent))
			require.NoError(t, service.kube.Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: agent.Namespace, Name: "workflow"}, Data: map[string]string{"workflow.yaml": string(document)}}))
			persisted, err := service.WorkflowRevision(t.Context(), client.ObjectKeyFromObject(agent))
			require.NoError(t, err)
			require.Equal(t, candidate, persisted)
			require.NoError(t, service.Agent(t.Context(), agent))
		})
	}
}

func TestWorkflowAdmissionValidatesCandidateReferenceGraph(t *testing.T) {
	for _, test := range []struct{ name, target, want string }{
		{"missing", "missing", "not found"},
		{"self cycle", "assistant", "cycle"},
		{"transitive cycle", "developer", "cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, agent, _ := admissionFixture(t, "kagent")
			child := agent.DeepCopy()
			child.Name, child.ResourceVersion = "developer", ""
			child.Spec.Template = v1alpha3.AgentTemplateSource{Inline: &v1alpha3.AgentTemplateSpec{WorkflowFrom: &v1alpha3.ConfigMapKeyReference{Name: "child-workflow", Key: "workflow.yaml"}}}
			require.NoError(t, service.kube.Create(t.Context(), child))
			require.NoError(t, service.kube.Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: agent.Namespace, Name: "child-workflow"}, Data: map[string]string{"workflow.yaml": string(nativeReferenceDocument(agent.Name))}}))
			agent.Spec.Template = v1alpha3.AgentTemplateSource{Inline: &v1alpha3.AgentTemplateSpec{WorkflowFrom: &v1alpha3.ConfigMapKeyReference{Name: "candidate", Key: "workflow.yaml"}}}
			_, err := service.Workflow(t.Context(), agent, nil, nativeReferenceDocument(test.target))
			require.ErrorContains(t, err, test.want)
		})
	}
}
