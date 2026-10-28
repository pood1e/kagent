package runner

import (
	"slices"
	"strings"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/tool"

	"github.com/kagent-dev/kagent/go/adk/pkg/mcp"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/harness/runtime"
)

func toolInventoryPlugin(observer runtime.ToolObserver, root string, remoteNames map[string]bool) (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{Name: "kagent-tool-inventory", BeforeModelCallback: func(ctx adkagent.Context, request *model.LLMRequest) (*model.LLMResponse, error) {
		if ctx.AgentName() == root {
			observer.ObserveTools(ctx, effectiveBuiltinTools(request.Tools, remoteNames))
		}
		return nil, nil
	}})
}

// effectiveBuiltinTools reads the SDK's model-facing registry after dynamic
// toolsets and automatic tools have been resolved.
func effectiveBuiltinTools(registry map[string]any, remoteNames map[string]bool) []*apiv1.DiscoveredTool {
	result := make([]*apiv1.DiscoveredTool, 0, len(registry))
	for name, value := range registry {
		if mcp.IsMCPTool(value) || remoteNames[name] {
			continue
		}
		item, ok := value.(tool.Tool)
		if !ok {
			continue
		}
		result = append(result, &apiv1.DiscoveredTool{Name: name, Description: item.Description()})
	}
	slices.SortFunc(result, func(a, b *apiv1.DiscoveredTool) int { return strings.Compare(a.Name, b.Name) })
	return result
}
