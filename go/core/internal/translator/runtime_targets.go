package translator

import (
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

// runtimeTargets preserves resolved selections for gateway authorization on the
// pinned revision, independently of each harness's configuration format.
func runtimeTargets(root *AgentInput) ([]v1alpha3.MCPToolBinding, []corev1.LocalObjectReference) {
	bindings := []v1alpha3.MCPToolBinding{}
	models := []corev1.LocalObjectReference{}
	var visit func(*AgentInput)
	visit = func(input *AgentInput) {
		for _, tool := range input.MCPTools {
			bindings = append(bindings, *tool.Binding.DeepCopy())
		}
		if input.ResolvedModelConfig != nil {
			models = append(models, corev1.LocalObjectReference{Name: input.ResolvedModelConfig.Config.Name})
		}
		for _, child := range input.Shared {
			visit(child.Agent)
		}
	}
	visit(root)
	slices.SortFunc(models, func(a, b corev1.LocalObjectReference) int { return strings.Compare(a.Name, b.Name) })
	return bindings, slices.Compact(models)
}
