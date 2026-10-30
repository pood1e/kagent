package translator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

// validateNativeWorkflowRefs walks raw behavior rather than prepared status, so
// cycles fail deterministically even before any referenced Agent becomes ready.
func (c *Compiler) validateNativeWorkflowRefs(ctx context.Context, root *v1alpha3.Agent, workflow *ResolvedWorkflow) error {
	visited := map[types.NamespacedName]bool{}
	var visit func(types.NamespacedName, *ResolvedWorkflow, []types.NamespacedName) error
	visit = func(key types.NamespacedName, current *ResolvedWorkflow, path []types.NamespacedName) error {
		if slices.Contains(path, key) {
			cycle := make([]string, 0, len(path)+1)
			for _, item := range append(path, key) {
				cycle = append(cycle, item.String())
			}
			return NewValidationError("native Agent workflow cycle: %s", strings.Join(cycle, " -> "))
		}
		if len(path) >= 32 {
			return NewValidationError("native Agent workflow exceeds 32 delegation levels")
		}
		if visited[key] || current == nil {
			return nil
		}
		path = append(slices.Clone(path), key)
		for field, node := range current.Spec.Nodes() {
			if node.AgentRef == nil {
				continue
			}
			targetKey := types.NamespacedName{Namespace: node.AgentRef.Namespace, Name: node.AgentRef.Name}
			target := FetchOne(c.ctx, c.collections.Agents, krt.FilterObjectName(targetKey))
			if target == nil {
				return fmt.Errorf("workflow %s %s.agent_ref: Agent %q not found", key, field, targetKey)
			}
			var template *TemplateConfiguration
			if (*target).Spec.Template.Inline != nil {
				template = &TemplateConfiguration{Name: (*target).Name, Namespace: (*target).Namespace, Spec: *(*target).Spec.Template.Inline.DeepCopy()}
			} else if ref := (*target).Spec.Template.Ref; ref != nil {
				templateKey := types.NamespacedName{Namespace: targetKey.Namespace, Name: ref.Name}
				found := FetchOne(c.ctx, c.collections.AgentTemplates, krt.FilterObjectName(templateKey))
				if found == nil {
					return fmt.Errorf("workflow Agent %q: AgentTemplate %q not found", targetKey, templateKey)
				}
				template = templateConfiguration(*found)
			} else {
				return NewValidationError("workflow Agent %q requires an inline or referenced template", targetKey)
			}
			nested, err := c.resolveWorkflow(ctx, template)
			if err != nil {
				return fmt.Errorf("workflow Agent %q: %w", targetKey, err)
			}
			if err := visit(targetKey, nested, path); err != nil {
				return err
			}
		}
		visited[key] = true
		return nil
	}
	return visit(types.NamespacedName{Namespace: root.Namespace, Name: root.Name}, workflow, nil)
}
