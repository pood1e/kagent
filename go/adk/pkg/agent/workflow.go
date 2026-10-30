package agent

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/adk/v2/agent"

	"github.com/kagent-dev/kagent/go/adk/pkg/declarative"
	"github.com/kagent-dev/kagent/go/adk/pkg/fileextract"
	"github.com/kagent-dev/kagent/go/adk/pkg/mcp"
	"github.com/kagent-dev/kagent/go/adk/pkg/sts"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
)

// createWorkflowAgent builds the declarative workflow that the controller
// delivered. The document names no Go code: its models are the ModelConfigs
// the controller resolved, and its MCP servers are the template's
// RemoteMCPServer bindings, registered under the server name.
func createWorkflowAgent(ctx context.Context, agentConfig *configv1.ADKAgentConfig, stsPlugin *sts.TokenPropagationPlugin, runtime *configv1.ADKRuntimeConfig) (agent.Agent, error) {
	workflow := agentConfig.GetWorkflow()
	reg := declarative.NewRegistry()
	models := workflow.GetModels()
	for _, name := range slices.Sorted(maps.Keys(models)) {
		llm, err := CreateLLM(ctx, models[name])
		if err != nil {
			return nil, fmt.Errorf("create workflow model %q: %w", name, err)
		}
		if err := reg.RegisterModel(name, fileextract.WithFileText(llm)); err != nil {
			return nil, err
		}
	}
	var headerProvider mcp.DynamicHeaderProvider
	if stsPlugin != nil {
		headerProvider = stsPlugin.HeaderProvider
	}
	toolsets, err := mcp.NamedToolsets(ctx, agentConfig.HttpTools, agentConfig.SseTools, runtime.GetTokenPropagation().GetEnabled(), headerProvider)
	if err != nil {
		return nil, fmt.Errorf("create workflow MCP toolsets: %w", err)
	}
	for _, ts := range toolsets {
		if err := reg.RegisterToolset(ts); err != nil {
			return nil, err
		}
	}
	spec, err := reg.ParseConfig(workflow)
	if err != nil {
		return nil, err
	}
	system, err := declarative.Build(ctx, spec, reg)
	if err != nil {
		return nil, err
	}
	return system.Root(), nil
}
