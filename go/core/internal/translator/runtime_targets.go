package translator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// runtimeTargets captures authoritative dependency UIDs as revision inputs.
// An all-tools binding retains server-use; Session admission expands it into
// exact tool grants before fixing the execution ceiling.
func runtimeTargets(agent *v1alpha3.Agent, input *HarnessInput) (*apiv1.ResourceGrantSet, error) {
	grants := &apiv1.ResourceGrantSet{}
	add := func(kind apiv1.AuthorizationScopeKind, namespace, name string, uid types.UID, permissions ...apiv1.Permission) {
		grants.Grants = append(grants.Grants, &apiv1.ResourceGrant{
			Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
				Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: kind, Ref: &apiv1.ResourceReference{Namespace: namespace, Name: name}, Uid: string(uid)},
			}}, Permissions: permissions,
		})
	}
	add(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT, agent.Namespace, agent.Name, agent.UID, apiv1.Permission_PERMISSION_AGENT_INVOKE)
	add(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_HARNESS, input.Harness.Namespace, input.Harness.Name, input.Harness.Source.UID, apiv1.Permission_PERMISSION_HARNESS_USE)
	var visit func(*AgentInput)
	visit = func(node *AgentInput) {
		if node.Template.Source != nil {
			add(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT_TEMPLATE, node.Template.Namespace, node.Template.Name, node.Template.Source.UID, apiv1.Permission_PERMISSION_TEMPLATE_USE)
		}
		for _, tool := range node.MCPTools {
			server := &apiv1.KubernetesAuthorizationReference{Kind: apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER,
				Ref: &apiv1.ResourceReference{Namespace: tool.Server.Namespace, Name: tool.Server.Name}, Uid: string(tool.Server.UID)}
			if len(tool.Binding.Tools) == 0 {
				add(server.Kind, server.Ref.Namespace, server.Ref.Name, tool.Server.UID, apiv1.Permission_PERMISSION_MCP_SERVER_USE)
			}
			for _, name := range tool.Binding.Tools {
				grants.Grants = append(grants.Grants, &apiv1.ResourceGrant{
					Resource: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{
						McpTool: &apiv1.MCPToolAuthorizationReference{Server: server, Name: name},
					}}, Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_TOOL_CALL},
				})
			}
		}
		if model := node.ResolvedModelConfig; model != nil {
			add(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION, model.Config.Namespace, model.Config.Name, model.Config.UID, apiv1.Permission_PERMISSION_MODEL_CONNECTION_USE)
		}
		for _, child := range node.Shared {
			visit(child.Agent)
		}
	}
	visit(input.Root)
	if input.Workflow != nil {
		if source := input.Root.Template.Source; source != nil {
			add(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT_TEMPLATE, input.Root.Template.Namespace, source.Name, source.UID, apiv1.Permission_PERMISSION_WORKFLOW_EXECUTE)
		} else {
			add(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT, agent.Namespace, agent.Name, agent.UID, apiv1.Permission_PERMISSION_WORKFLOW_EXECUTE)
		}
		for _, model := range input.Workflow.Models {
			add(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION, model.Config.Namespace, model.Config.Name, model.Config.UID, apiv1.Permission_PERMISSION_MODEL_CONNECTION_USE)
		}
	}
	result, err := auth.CanonicalResourceGrants(grants)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime dependency identities: %w", err)
	}
	return result, nil
}
