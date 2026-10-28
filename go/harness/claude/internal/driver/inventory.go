package driver

import (
	"context"
	"slices"
	"strings"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

func (d *ProcessDriver) observeTools(ctx context.Context, names *[]string) {
	if names == nil || d.config.ToolObserver == nil {
		return
	}
	tools := make([]*apiv1.DiscoveredTool, 0, len(*names))
	for _, name := range *names {
		if strings.HasPrefix(name, "mcp__") || slices.Contains(d.config.DisallowedTools, name) {
			continue
		}
		tools = append(tools, &apiv1.DiscoveredTool{Name: name})
	}
	d.config.ToolObserver.ObserveTools(ctx, tools)
}
