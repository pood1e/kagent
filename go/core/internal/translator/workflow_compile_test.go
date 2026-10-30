package translator_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	runtimeconfig "github.com/kagent-dev/kagent/go/adk/pkg/config"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/translator"
)

const workflowYAML = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: support
    agent_class: Workflow
    entry: triage
    nodes:
      - { id: triage, type: function, function: triage }
`

func workflowConfigMap(document string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "workflows", Namespace: "test", UID: "workflows-uid"},
		Data:       map[string]string{"agents.yaml": document},
	}
}

func byoHarness() *v1alpha3.Harness {
	return &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Name: "byo", Namespace: "test"}, Spec: v1alpha3.HarnessSpec{
		BYO: &v1alpha3.BYOHarness{},
		Workload: v1alpha3.HarnessWorkload{
			Image:   new("example.com/agent@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			Command: []string{"/agent"},
		},
		Substrate: v1alpha3.RuntimeSubstratePolicy{WorkerPoolRef: corev1.LocalObjectReference{Name: "default"}},
	}}
}

func workflowTemplate(mutate func(*v1alpha3.AgentTemplateSpec)) *v1alpha3.AgentTemplate {
	template := &v1alpha3.AgentTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "test"},
		Spec: v1alpha3.AgentTemplateSpec{
			WorkflowFrom: &v1alpha3.ConfigMapKeyReference{Name: "workflows", Key: "agents.yaml"},
		},
	}
	if mutate != nil {
		mutate(&template.Spec)
	}
	return template
}

func TestCompileAgentDeliversWorkflowToBYO(t *testing.T) {
	harness := byoHarness()
	configMap := workflowConfigMap(workflowYAML)
	template := workflowTemplate(nil)

	revision, err := compiler(t, configMap, harness).CompileAgent(t.Context(), inlineAgent(harness, template))
	require.NoError(t, err)

	var config configv1.ADKConfig
	require.NoError(t, runtimeconfig.Unmarshal(revision.ConfigJSON, &config))
	workflow := config.Agent.Workflow
	require.NotNil(t, workflow)
	require.JSONEq(t, `{"apiVersion":"adk.declarative/v0.2","kind":"AgentSystem","agents":[{"name":"support","agent_class":"Workflow","entry":"triage","nodes":[{"id":"triage","type":"function","function":"triage"}]}]}`,
		string(workflow.Document))
	require.Len(t, workflow.Sha256, 64)
	require.Contains(t, string(revision.Provenance), `"kind":"ConfigMap"`, "the source ConfigMap is recorded")

	original, err := revision.Digest()
	require.NoError(t, err)
	configMap.Data["agents.yaml"] = workflowYAML + "      - { id: extra, type: function, function: extra }\n    edges: [{ from: triage, to: extra }]\n"
	changed, err := compiler(t, configMap, harness).CompileAgent(t.Context(), inlineAgent(harness, template))
	require.NoError(t, err)
	updated, err := changed.Digest()
	require.NoError(t, err)
	require.NotEqual(t, original, updated, "a changed document must prepare a new revision")
}

func TestCompileAgentWithoutWorkflowSendsNone(t *testing.T) {
	harness := byoHarness()
	revision, err := compiler(t, harness).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(func(s *v1alpha3.AgentTemplateSpec) {
		s.WorkflowFrom = nil
		s.SystemPrompt = "be helpful"
	})))
	require.NoError(t, err)
	var config configv1.ADKConfig
	require.NoError(t, runtimeconfig.Unmarshal(revision.ConfigJSON, &config))
	require.Nil(t, config.Agent.Workflow)
}

func TestCompileAgentRejectsInvalidWorkflow(t *testing.T) {
	harness := byoHarness()
	const invalid = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: support
    agent_class: Workflow
    nodes:
      - { id: triage, type: function }
    edges:
      - { from: START, to: triage }
      - { from: triage, to: ghost }
`
	_, err := compiler(t, workflowConfigMap(invalid), harness).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(nil)))
	var validation *translator.ValidationError
	require.ErrorAs(t, err, &validation, "an invalid document is a compatibility problem, not a retryable lookup failure")
	require.ErrorContains(t, err, "invalid workflow")
	require.ErrorContains(t, err, "agents[0].nodes[0].function: required for node type function")
	require.ErrorContains(t, err, `agents[0].edges[1].to: unknown node "ghost"`)
}

func TestCompileAgentWorkflowSourceFailuresAreNotValidationErrors(t *testing.T) {
	harness := byoHarness()
	tests := []struct {
		name    string
		objects []any
		wantErr string
	}{
		{name: "missing ConfigMap", objects: []any{harness}, wantErr: `resolve workflow ConfigMap "workflows": not found`},
		{name: "missing key", objects: []any{harness, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "workflows", Namespace: "test"}}}, wantErr: `resolve workflow ConfigMap "workflows": key "agents.yaml" not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compiler(t, tt.objects...).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(nil)))
			require.ErrorContains(t, err, tt.wantErr)
			var validation *translator.ValidationError
			require.False(t, errors.As(err, &validation), "a ConfigMap that may appear later must be retried")
		})
	}
}

func TestCompileAgentRejectsWorkflowOnOtherHarnesses(t *testing.T) {
	configMap := workflowConfigMap(workflowYAML)
	for name, spec := range map[string]v1alpha3.HarnessSpec{
		"codex":  {Codex: &v1alpha3.CodexHarness{}},
		"claude": {Claude: &v1alpha3.ClaudeHarness{}},
	} {
		t.Run(name, func(t *testing.T) {
			spec.Substrate = v1alpha3.RuntimeSubstratePolicy{WorkerPoolRef: corev1.LocalObjectReference{Name: "default"}}
			harness := &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test"}, Spec: spec}
			// No ModelConfig: managed harnesses would also reject that, and the
			// workflow gate must be the reported reason.
			_, err := compiler(t, configMap, harness).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(nil)))
			var validation *translator.ValidationError
			require.ErrorAs(t, err, &validation)
			require.ErrorContains(t, err, "does not run declarative workflows; use a kagent or BYO Harness")
		})
	}
}

// kagentWorkflowYAML names only what the kagent runtime offers: ModelConfigs
// and the template's RemoteMCPServer bindings.
const kagentWorkflowYAML = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: rca
    agent_class: Workflow
    entry: diagnose
    nodes:
      - { id: diagnose, type: llm, model: default-model, instruction: Diagnose., tools: [incidents] }
      - { id: review, type: llm, model: review-model, instruction: Review. }
      - { id: args, type: cel, output: "{'summary': input}" }
      - { id: record, type: tool, server: incidents, tool: record_analysis }
      - { id: done, type: cel, output: "'recorded'" }
    edges:
      - { from: diagnose, to: review }
      - { from: review, to: args }
      - { from: args, to: record }
      - { from: record, to: done }
`

func kagentHarness() *v1alpha3.Harness {
	return &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Name: "kagent", Namespace: "test"}, Spec: v1alpha3.HarnessSpec{
		Kagent:    &v1alpha3.KagentHarness{},
		Substrate: v1alpha3.RuntimeSubstratePolicy{WorkerPoolRef: corev1.LocalObjectReference{Name: "default"}},
	}}
}

func kagentWorkflowTemplate(mutate func(*v1alpha3.AgentTemplateSpec)) *v1alpha3.AgentTemplate {
	return workflowTemplate(func(s *v1alpha3.AgentTemplateSpec) {
		s.ModelConfig = &corev1.LocalObjectReference{Name: "default-model"}
		s.Tools = []v1alpha3.ToolBinding{{MCP: &v1alpha3.MCPToolBinding{
			Server: corev1.TypedLocalObjectReference{Kind: "RemoteMCPServer", Name: "incidents"}, Tools: []string{"record_analysis"},
		}}}
		if mutate != nil {
			mutate(s)
		}
	})
}

func reviewModel() *v1alpha3.ModelConfig {
	model := modelConfig()
	model.Name, model.Spec.Model = "review-model", "gpt-4o-mini"
	return model
}

func TestCompileAgentRunsWorkflowOnKagent(t *testing.T) {
	harness := kagentHarness()
	objects := []any{workflowConfigMap(kagentWorkflowYAML), modelConfig(), reviewModel(), remoteMCPServer("incidents", "http://incidents.test/mcp"), harness}

	revision, err := compiler(t, objects...).CompileAgent(t.Context(), inlineAgent(harness, kagentWorkflowTemplate(nil)))
	require.NoError(t, err)

	var config configv1.ADKConfig
	require.NoError(t, runtimeconfig.Unmarshal(revision.ConfigJSON, &config))
	workflow := config.Agent.Workflow
	require.NotNil(t, workflow)
	require.Len(t, workflow.Models, 2)
	require.Equal(t, "gpt-4o", workflow.Models["default-model"].Model)
	require.Equal(t, "gpt-4o-mini", workflow.Models["review-model"].Model)
	require.Equal(t, "incidents", config.Agent.HttpTools[0].Name, "the document names the server of the binding")
	require.Equal(t, []corev1.LocalObjectReference{{Name: "default-model"}, {Name: "review-model"}}, revision.Models, "the gateway admits every model the document names")
	require.Contains(t, string(revision.Provenance), `"name":"review-model"`)
	require.Contains(t, string(revision.Provenance), "workflows-uid")
}

func TestCompileAgentResolvesTemplateNamesInKagentWorkflows(t *testing.T) {
	const document = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - { name: analyst, agent_class: LlmAgent, model: "template:model", instruction: Analyze., tools: ["template:tools"] }
`
	harness := kagentHarness()
	objects := []any{workflowConfigMap(document), modelConfig(), remoteMCPServer("incidents", "http://incidents.test/mcp"), harness}

	revision, err := compiler(t, objects...).CompileAgent(t.Context(), inlineAgent(harness, kagentWorkflowTemplate(nil)))
	require.NoError(t, err)

	var config configv1.ADKConfig
	require.NoError(t, runtimeconfig.Unmarshal(revision.ConfigJSON, &config))
	require.Equal(t, "gpt-4o", config.Agent.Workflow.Models["template:model"].Model, "the template's own ModelConfig")
	require.Equal(t, []corev1.LocalObjectReference{{Name: "default-model"}}, revision.Models)
}

func TestCompileAgentRejectsKagentWorkflowsItCannotBuild(t *testing.T) {
	const hitlYAML = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: approve
    agent_class: Workflow
    entry: ask
    nodes:
      - { id: ask, type: hitl, message: "Approve?" }
`
	const declaredServerYAML = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
mcp_servers:
  - { name: crm, url: "http://crm.test/mcp" }
agents:
  - { name: support, agent_class: LlmAgent, model: default-model, instruction: hi, tools: [crm] }
`
	const unboundServerYAML = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - { name: support, agent_class: LlmAgent, model: default-model, instruction: hi, tools: [crm] }
`
	tests := []struct {
		name     string
		document string
		mutate   func(*v1alpha3.AgentTemplateSpec)
		want     string
	}{
		{name: "Go function", document: workflowYAML, want: `agents[0].nodes[0].function: function "triage" is not registered`},
		{name: "unbound server", document: unboundServerYAML, want: `agents[0].tools[0]: tool "crm" is not registered`},
		{name: "declared server", document: declaredServerYAML, want: "RemoteMCPServer bindings"},
		{name: "hitl node", document: hitlYAML, want: "agents[0].nodes[0]: hitl nodes are not supported"},
		{name: "approval", document: kagentWorkflowYAML, mutate: func(s *v1alpha3.AgentTemplateSpec) {
			s.Tools[0].MCP.RequireApproval = true
		}, want: `RemoteMCPServer "incidents" requires approval`},
		{name: "output schema", document: kagentWorkflowYAML, mutate: func(s *v1alpha3.AgentTemplateSpec) {
			s.OutputSchema = &apiextensionsv1.JSON{Raw: []byte(`{"type":"object"}`)}
		}, want: "output schema"},
		{name: "skills", document: kagentWorkflowYAML, mutate: func(s *v1alpha3.AgentTemplateSpec) {
			s.Skills = []v1alpha3.AgentTemplateSkill{{Name: "review", Source: v1alpha3.ArtifactSource{
				OCI: "ghcr.io/acme/review@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			}}}
		}, want: "skills or plugins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := kagentHarness()
			objects := []any{workflowConfigMap(tt.document), modelConfig(), reviewModel(), remoteMCPServer("incidents", "http://incidents.test/mcp"), harness}
			_, err := compiler(t, objects...).CompileAgent(t.Context(), inlineAgent(harness, kagentWorkflowTemplate(tt.mutate)))
			var validation *translator.ValidationError
			require.ErrorAs(t, err, &validation)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestCompileAgentRetriesMissingWorkflowModels(t *testing.T) {
	harness := kagentHarness()
	objects := []any{workflowConfigMap(kagentWorkflowYAML), modelConfig(), remoteMCPServer("incidents", "http://incidents.test/mcp"), harness}
	_, err := compiler(t, objects...).CompileAgent(t.Context(), inlineAgent(harness, kagentWorkflowTemplate(nil)))
	require.ErrorContains(t, err, `resolve ModelConfig "review-model": not found`)
	var validation *translator.ValidationError
	require.False(t, errors.As(err, &validation), "a ModelConfig that may appear later must be retried")
}

func TestCompileAgentRejectsWorkflowOnSharedTemplates(t *testing.T) {
	harness := byoHarness()
	child := &v1alpha3.AgentTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "test"},
		Spec: v1alpha3.AgentTemplateSpec{
			WorkflowFrom: &v1alpha3.ConfigMapKeyReference{Name: "workflows", Key: "agents.yaml"},
		},
	}
	root := workflowTemplate(func(s *v1alpha3.AgentTemplateSpec) {
		s.WorkflowFrom = nil
		s.Tools = []v1alpha3.ToolBinding{{SubAgent: &v1alpha3.SubAgentToolBinding{
			Name: "child", Description: "delegate", TemplateRef: &corev1.LocalObjectReference{Name: child.Name},
		}}}
	})
	_, err := compiler(t, workflowConfigMap(workflowYAML), child, harness).CompileAgent(t.Context(), inlineAgent(harness, root))
	var validation *translator.ValidationError
	require.ErrorAs(t, err, &validation)
	require.ErrorContains(t, err, `AgentTemplate "child" sets workflowFrom, which is only supported on the root template`)
}

const promptWorkflowYAML = `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
agents:
  - name: support
    agent_class: LlmAgent
    model: stub
    instruction_from: { %s: { name: prompts, key: support.md } }
`

func TestCompileAgentInlinesWorkflowPromptsFromConfigMaps(t *testing.T) {
	harness := byoHarness()
	prompts := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "prompts", Namespace: "test", UID: "prompts-uid"},
		Data:       map[string]string{"support.md": "Answer support tickets."},
	}
	document := fmt.Sprintf(promptWorkflowYAML, "config_map")
	revision, err := compiler(t, workflowConfigMap(document), prompts, harness).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(nil)))
	require.NoError(t, err)

	var config configv1.ADKConfig
	require.NoError(t, runtimeconfig.Unmarshal(revision.ConfigJSON, &config))
	require.Contains(t, string(config.Agent.Workflow.Document), `"instruction":"Answer support tickets."`)
	require.NotContains(t, string(config.Agent.Workflow.Document), "instruction_from")
	require.Contains(t, string(revision.Provenance), "prompts-uid", "the prompt ConfigMap is recorded")

	_, err = compiler(t, workflowConfigMap(document), harness).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(nil)))
	require.ErrorContains(t, err, `resolve workflow prompt ConfigMap "prompts": not found`)
	var validation *translator.ValidationError
	require.False(t, errors.As(err, &validation), "a prompt ConfigMap that may appear later must be retried")
}

func TestCompileAgentRejectsMCPConfigMapSources(t *testing.T) {
	harness := byoHarness()
	document := `
apiVersion: adk.declarative/v0.2
kind: AgentSystem
mcp_servers:
  - name: crm
    url: http://crm
    headers:
      - name: Authorization
        value_from: { config_map: { name: credentials, key: token } }
agents:
  - name: support
    agent_class: LlmAgent
    model: stub
    instruction: hi
`
	_, err := compiler(t, workflowConfigMap(document), harness).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(nil)))
	var validation *translator.ValidationError
	require.ErrorAs(t, err, &validation)
	require.ErrorContains(t, err, "mcp_servers[0].headers[0].value_from: must use env")
}

func TestCompileAgentRejectsWorkflowPromptsFromSecrets(t *testing.T) {
	harness := byoHarness()
	_, err := compiler(t, workflowConfigMap(fmt.Sprintf(promptWorkflowYAML, "secret")), harness).CompileAgent(t.Context(), inlineAgent(harness, workflowTemplate(nil)))
	var validation *translator.ValidationError
	require.ErrorAs(t, err, &validation)
	require.ErrorContains(t, err, "agents[0].instruction_from")
	require.ErrorContains(t, err, "use a ConfigMap")
}
