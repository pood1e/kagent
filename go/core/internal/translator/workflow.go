package translator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"

	"github.com/kagent-dev/kagent/go/adk/pkg/declarative"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

// maxWorkflowBytes bounds a declarative workflow document, before and after
// canonicalization. The runtime config carries it in a bounded protobuf field.
const maxWorkflowBytes = 64 << 10

// ResolvedWorkflow is a declarative workflow document that passed structural
// validation, in the canonical form delivered to the runtime. Source is the
// ConfigMap key it was read from; Prompts are the ConfigMap keys whose text
// was inlined for instruction_from and global_instruction_from.
type ResolvedWorkflow struct {
	Document []byte
	SHA256   string
	Source   *ResolvedConfigMapKeyReference
	Prompts  []*ResolvedConfigMapKeyReference
	// Spec is the validated document with its prompt sources inlined.
	Spec *declarative.Spec
	// Models are the ModelConfigs a document for the kagent runtime names,
	// keyed by name. A BYO image resolves model names itself.
	Models map[string]*ResolvedModelConfig
}

// ConfigMapNames returns the ConfigMaps the workflow was compiled from.
func (w *ResolvedWorkflow) ConfigMapNames() []string {
	if w == nil {
		return nil
	}
	var names []string
	if w.Source != nil {
		names = append(names, w.Source.NamespacedName.Name)
	}
	for _, p := range w.Prompts {
		names = append(names, p.NamespacedName.Name)
	}
	return names
}

// promptReferenceError is a prompt ConfigMap lookup failure. The ConfigMap
// may appear later, so it is retried rather than reported as incompatible.
type promptReferenceError struct{ err error }

func (e *promptReferenceError) Error() string { return e.err.Error() }
func (e *promptReferenceError) Unwrap() error { return e.err }

// resolveWorkflow reads and validates the template's workflow document. It
// returns nil when the template declares none.
func (c *Compiler) resolveWorkflow(_ context.Context, template *TemplateConfiguration) (*ResolvedWorkflow, error) {
	ref := template.Spec.WorkflowFrom
	if ref == nil {
		return nil, nil
	}
	raw, source, err := c.resolveConfigMapKey(template.Namespace, "workflow", ref)
	if err != nil {
		return nil, err
	}
	var prompts []*ResolvedConfigMapKeyReference
	resolve := func(_ string, src declarative.TextSource) (string, *declarative.TextSource, error) {
		if src.ConfigMap == nil {
			return "", nil, unsupportedPromptSource(src)
		}
		text, prompt, err := c.resolveConfigMapKey(template.Namespace, "workflow prompt", &v1alpha3.ConfigMapKeyReference{Name: src.ConfigMap.Name, Key: src.ConfigMap.Key})
		if err != nil {
			return "", nil, &promptReferenceError{err: err}
		}
		prompts = append(prompts, prompt)
		return string(text), nil, nil
	}
	spec, canonical, err := validateWorkflow(raw, resolve)
	if err != nil {
		if _, ok := errors.AsType[*promptReferenceError](err); ok {
			return nil, fmt.Errorf("workflow: %w", err)
		}
		return nil, NewValidationError("invalid workflow: %v", err)
	}
	digest := sha256.Sum256(canonical)
	return &ResolvedWorkflow{Document: canonical, SHA256: hex.EncodeToString(digest[:]), Source: source, Prompts: prompts, Spec: spec}, nil
}

// resolveKagentWorkflow checks that the kagent Go runtime can build the
// document from the revision alone and resolves the ModelConfigs it names.
// That runtime has no Go registry: a document names models by ModelConfig and
// MCP servers by the RemoteMCPServer of a template binding. The runtime cannot
// pause a workflow, so hitl nodes and bindings that require approval are
// refused.
func (c *Compiler) resolveKagentWorkflow(workflow *ResolvedWorkflow, root *AgentInput, outputSchema *ResolvedOutputSchema) error {
	template := root.Template
	switch {
	case outputSchema != nil:
		return NewValidationError("workflowFrom cannot be combined with an output schema on a kagent Harness; set output_schema on the document's terminal node")
	case len(root.Shared) > 0:
		return NewValidationError("workflowFrom cannot be combined with subagent tools on a kagent Harness; declare the agents in the document")
	case len(template.Spec.Skills) > 0 || len(template.Spec.Plugins) > 0:
		return NewValidationError("workflowFrom cannot be combined with skills or plugins on a kagent Harness")
	case len(workflow.Spec.MCPServers) > 0:
		return NewValidationError("invalid workflow: mcp_servers: a kagent Harness reaches MCP servers through the AgentTemplate's RemoteMCPServer bindings; name a bound server instead")
	}
	for path, node := range workflow.Spec.Nodes() {
		if node.Type == declarative.NodeHITL {
			return NewValidationError("invalid workflow: %s: hitl nodes are not supported on a kagent Harness", path)
		}
	}
	vocabulary := declarative.NewRegistry()
	bound := map[string]bool{}
	for _, binding := range root.MCPTools {
		name := binding.Server.Name
		if binding.Binding.RequireApproval {
			return NewValidationError("RemoteMCPServer %q requires approval, which a workflow on a kagent Harness cannot pause for", name)
		}
		if bound[name] {
			continue
		}
		bound[name] = true
		if err := vocabulary.RegisterToolset(boundServer(name)); err != nil {
			return NewValidationError("invalid workflow: %v", err)
		}
	}
	models := make(map[string]*ResolvedModelConfig)
	for _, name := range workflow.Spec.Models() {
		resolved, err := c.resolveModelConfig(template.Namespace, name)
		if err != nil {
			return err
		}
		if err := vocabulary.RegisterModel(name, declarative.NewStubModel(name)); err != nil {
			return NewValidationError("invalid workflow: %v", err)
		}
		models[name] = resolved
	}
	if err := vocabulary.Validate(workflow.Spec); err != nil {
		return NewValidationError("invalid workflow for the kagent runtime: %v", err)
	}
	workflow.Models = models
	return nil
}

// boundServer stands in for the toolset the kagent runtime registers for a
// bound RemoteMCPServer, so validation resolves the names the runtime does.
type boundServer string

func (s boundServer) Name() string { return string(s) }

func (boundServer) Tools(agent.ReadonlyContext) ([]tool.Tool, error) { return nil, nil }

// unsupportedPromptSource explains why the controller does not deliver a
// Secret or environment prompt source. Secrets never enter a runtime: kagent
// injects credentials only at the egress gateway, so prompt text that must be
// visible to the runtime belongs in a ConfigMap. Env sources are reserved for
// what the runtime image itself sets and are not resolvable here.
func unsupportedPromptSource(src declarative.TextSource) error {
	if src.Secret != nil {
		return fmt.Errorf("secret %q: prompts cannot be read from Secrets because kagent never delivers Secret values to a runtime; use a ConfigMap", src.Secret.Name)
	}
	return fmt.Errorf("env %q: environment prompt sources are not supported in a delivered workflow; use a ConfigMap", src.Env)
}

// validateWorkflow checks a document with the same structural rules the Go ADK
// applies before building it, resolves its prompt sources with resolve, and
// returns it with its canonical JSON. Names that only a BYO image can resolve
// (functions, tools, models) are not checked here; the image reports them at
// startup.
func validateWorkflow(raw []byte, resolve declarative.PromptResolver) (*declarative.Spec, []byte, error) {
	if len(raw) == 0 {
		return nil, nil, fmt.Errorf("document is empty")
	}
	if len(raw) > maxWorkflowBytes {
		return nil, nil, fmt.Errorf("document exceeds %d bytes", maxWorkflowBytes)
	}
	spec, err := declarative.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	if err := declarative.ResolvePrompts(spec, resolve); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize document: %w", err)
	}
	if len(canonical) > maxWorkflowBytes {
		return nil, nil, fmt.Errorf("canonical document exceeds %d bytes", maxWorkflowBytes)
	}
	return spec, canonical, nil
}
