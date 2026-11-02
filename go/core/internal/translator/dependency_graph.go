package translator

import (
	"context"
	"fmt"

	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// runtimeDependencies includes explicitly delegated native Agents, so a child
// Session can use only capabilities captured by its root's prepared graph.
func (c *Compiler) runtimeDependencies(ctx context.Context, root *v1alpha3.Agent, input *HarnessInput) (*apiv1.ExecutionDependencyManifest, error) {
	result := &apiv1.ResourceGrantSet{}
	var direct *apiv1.ResourceGrantSet
	visited := map[types.NamespacedName]bool{}
	var visit func(*v1alpha3.Agent, *HarnessInput, int) error
	visit = func(agent *v1alpha3.Agent, input *HarnessInput, depth int) error {
		key := types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}
		if visited[key] {
			return nil
		}
		if depth >= 32 {
			return NewValidationError("runtime dependency graph exceeds 32 delegation levels")
		}
		visited[key] = true
		grants, err := runtimeTargets(agent, input)
		if err != nil {
			return err
		}
		if agent == root {
			direct = grants
		} else {
			result.Grants = append(result.Grants, grants.Grants...)
		}
		if input.Workflow == nil {
			return nil
		}
		for _, node := range input.Workflow.Spec.Nodes() {
			if node.AgentRef == nil {
				continue
			}
			ref := types.NamespacedName{Namespace: node.AgentRef.Namespace, Name: node.AgentRef.Name}
			target := FetchOne(c.ctx, c.collections.Agents, krt.FilterObjectName(ref))
			if target == nil {
				return fmt.Errorf("resolve delegated Agent %s: not found", ref)
			}
			if visited[ref] {
				continue
			}
			harness, template, err := c.agentConfiguration(*target)
			if err != nil {
				return err
			}
			tree, err := c.resolveTree(ctx, harness, template)
			if err != nil {
				return err
			}
			child, err := c.buildInputs(ctx, tree)
			if err != nil {
				return err
			}
			if err := visit(*target, child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, input, 0); err != nil {
		return nil, err
	}
	delegated, err := auth.CanonicalResourceGrants(result)
	if err != nil {
		return nil, err
	}
	return &apiv1.ExecutionDependencyManifest{Direct: direct, Delegated: delegated}, nil
}
