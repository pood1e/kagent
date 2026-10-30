package config

// Names that a declarative workflow on the kagent harness uses for its
// AgentTemplate's own model and tools, so that a document need not know the
// names a deployment gives them.
const (
	// WorkflowTemplateModel names the AgentTemplate's ModelConfig.
	WorkflowTemplateModel = "template:model"
	// WorkflowTemplateTools names one toolset of every MCP server bound in the
	// AgentTemplate's tools.
	WorkflowTemplateTools = "template:tools"
)
