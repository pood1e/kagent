package grpcserver

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func verifyNativeDefinitionPermissions(
	t *testing.T,
	access apiv1.AuthorizationServiceClient,
	harnesses apiv1.HarnessServiceClient,
	templates apiv1.AgentTemplateServiceClient,
	models apiv1.ModelServiceClient,
	admin, alice context.Context,
	subject *apiv1.SubjectReference,
	workspace *apiv1.WorkspaceAuthorizationReference,
) {
	t.Helper()
	workspaceResource := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{Workspace: workspace}}
	resource := func(kind apiv1.AuthorizationScopeKind, name string) *apiv1.AuthorizationResource {
		return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
			Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: kind,
				Ref: &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: name}},
		}}
	}
	grant := func(role apiv1.AccessRole, scope *apiv1.AuthorizationResource) *apiv1.RoleBinding {
		t.Helper()
		result, err := access.GrantRole(admin, &apiv1.GrantRoleRequest{Workspace: workspace, RequestId: uuid.NewString(),
			Binding: &apiv1.RoleBinding{Subject: subject, Role: role, Scope: scope}})
		require.NoError(t, err)
		return result.Binding
	}
	revoke := func(binding *apiv1.RoleBinding) {
		t.Helper()
		_, err := access.RevokeRole(admin, &apiv1.RevokeRoleRequest{Workspace: workspace, RequestId: uuid.NewString(), Binding: binding})
		require.NoError(t, err)
	}
	harnessRef := &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: "runtime"}
	visible, err := harnesses.ListHarnesses(alice, &apiv1.ListHarnessesRequest{Namespace: workspace.Namespace})
	require.NoError(t, err)
	require.Empty(t, visible.Harnesses)
	_, err = harnesses.GetHarness(alice, &apiv1.GetHarnessRequest{Ref: harnessRef})
	assertGRPCCode(t, err, codes.PermissionDenied)
	consumer := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_HARNESS, harnessRef.Name))
	_, err = harnesses.GetHarness(alice, &apiv1.GetHarnessRequest{Ref: harnessRef})
	assertGRPCCode(t, err, codes.PermissionDenied)
	revoke(consumer)
	reader := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_READER, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_HARNESS, harnessRef.Name))
	visible, err = harnesses.ListHarnesses(alice, &apiv1.ListHarnessesRequest{Namespace: workspace.Namespace})
	require.NoError(t, err)
	require.Len(t, visible.Harnesses, 1)
	_, err = harnesses.GetHarness(alice, &apiv1.GetHarnessRequest{Ref: harnessRef})
	require.NoError(t, err)
	_, err = harnesses.UpdateHarness(alice, &apiv1.UpdateHarnessRequest{Ref: harnessRef, Resource: structured(t, testHarness(workspace.Namespace, harnessRef.Name, "pool"), harnessKind)})
	assertGRPCCode(t, err, codes.PermissionDenied)
	revoke(reader)
	visible, err = harnesses.ListHarnesses(alice, &apiv1.ListHarnessesRequest{Namespace: workspace.Namespace})
	require.NoError(t, err)
	require.Empty(t, visible.Harnesses)
	grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_MANAGER, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_HARNESS, harnessRef.Name))
	_, err = harnesses.UpdateHarness(alice, &apiv1.UpdateHarnessRequest{Ref: harnessRef, Resource: structured(t, testHarness(workspace.Namespace, harnessRef.Name, "pool"), harnessKind)})
	require.NoError(t, err)
	_, err = harnesses.DeleteHarness(alice, &apiv1.DeleteHarnessRequest{Ref: harnessRef})
	require.NoError(t, err)
	_, err = harnesses.CreateHarness(alice, &apiv1.CreateHarnessRequest{
		Ref:      &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: "new-runtime"},
		Resource: structured(t, testHarness(workspace.Namespace, "new-runtime", "pool"), harnessKind),
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	workspaceManager := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_MANAGER, workspaceResource)
	_, err = harnesses.CreateHarness(alice, &apiv1.CreateHarnessRequest{
		Ref:      &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: "new-runtime"},
		Resource: structured(t, testHarness(workspace.Namespace, "new-runtime", "pool"), harnessKind),
	})
	require.NoError(t, err)
	revoke(workspaceManager)

	templateRef := &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: "behavior"}
	visibleTemplates, err := templates.ListAgentTemplates(alice, &apiv1.ListAgentTemplatesRequest{Namespace: workspace.Namespace})
	require.NoError(t, err)
	require.Empty(t, visibleTemplates.AgentTemplates)
	templateReader := grant(apiv1.AccessRole_ACCESS_ROLE_DEFINITION_READER, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT_TEMPLATE, templateRef.Name))
	visibleTemplates, err = templates.ListAgentTemplates(alice, &apiv1.ListAgentTemplatesRequest{Namespace: workspace.Namespace})
	require.NoError(t, err)
	require.Len(t, visibleTemplates.AgentTemplates, 1)
	_, err = templates.GetAgentTemplate(alice, &apiv1.GetAgentTemplateRequest{Ref: templateRef})
	require.NoError(t, err)
	revoke(templateReader)
	grant(apiv1.AccessRole_ACCESS_ROLE_DEFINITION_EDITOR, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_AGENT_TEMPLATE, templateRef.Name))
	_, err = templates.UpdateAgentTemplate(alice, &apiv1.UpdateAgentTemplateRequest{
		Ref: templateRef, Resource: structured(t, testAgentTemplate(workspace.Namespace, templateRef.Name, "primary"), "AgentTemplate"),
	})
	require.NoError(t, err)
	_, err = templates.DeleteAgentTemplate(alice, &apiv1.DeleteAgentTemplateRequest{Ref: templateRef})
	require.NoError(t, err)

	modelRef := &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: "primary"}
	_, err = models.GetModelConfig(alice, &apiv1.GetModelConfigRequest{Ref: modelRef})
	assertGRPCCode(t, err, codes.PermissionDenied)
	modelConsumer := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_USER, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION, modelRef.Name))
	_, err = models.GetModelConfig(alice, &apiv1.GetModelConfigRequest{Ref: modelRef})
	assertGRPCCode(t, err, codes.PermissionDenied)
	revoke(modelConsumer)
	modelReader := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_READER, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION, modelRef.Name))
	visibleModels, err := models.ListModelConfigs(alice, &apiv1.ListModelConfigsRequest{})
	require.NoError(t, err)
	require.Len(t, visibleModels.ModelConfigs, 1)
	_, err = models.GetModelConfig(alice, &apiv1.GetModelConfigRequest{Ref: modelRef})
	require.NoError(t, err)
	_, err = models.UpdateModelConfig(alice, &apiv1.UpdateModelConfigRequest{Ref: modelRef,
		Resource: modelConfigResource(t, &v1alpha3.ModelConfig{Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "gpt-4"}}),
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	revoke(modelReader)
	grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_MANAGER, resource(apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION, modelRef.Name))
	_, err = models.UpdateModelConfig(alice, &apiv1.UpdateModelConfigRequest{Ref: modelRef,
		Resource: modelConfigResource(t, &v1alpha3.ModelConfig{Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "gpt-4"}}),
	})
	require.NoError(t, err)
	_, err = models.DeleteModelConfig(alice, &apiv1.DeleteModelConfigRequest{Ref: modelRef})
	require.NoError(t, err)
	_, err = models.CreateModelConfig(alice, &apiv1.CreateModelConfigRequest{Ref: &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: "new-primary"},
		Resource: modelConfigResource(t, &v1alpha3.ModelConfig{Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "gpt-4"}}),
	})
	assertGRPCCode(t, err, codes.PermissionDenied)
	workspaceModelManager := grant(apiv1.AccessRole_ACCESS_ROLE_DEPENDENCY_MANAGER, workspaceResource)
	_, err = models.CreateModelConfig(alice, &apiv1.CreateModelConfigRequest{Ref: &apiv1.ResourceReference{Namespace: workspace.Namespace, Name: "new-primary"},
		Resource: modelConfigResource(t, &v1alpha3.ModelConfig{Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "gpt-4"}}),
	})
	require.NoError(t, err)
	revoke(workspaceModelManager)
}
