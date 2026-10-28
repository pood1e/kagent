package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool/functiontool"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type inventoryObserver struct{ tools [][]*apiv1.DiscoveredTool }

func (o *inventoryObserver) ObserveTools(_ context.Context, tools []*apiv1.DiscoveredTool) {
	o.tools = append(o.tools, tools)
}

type inventoryContext struct {
	adkagent.Context
	name string
}

func (c inventoryContext) AgentName() string { return c.name }

func TestToolInventoryObservesOnlyEffectiveRootTools(t *testing.T) {
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read a file"}, func(adkagent.Context, struct{}) (string, error) { return "", nil })
	require.NoError(t, err)
	remote, err := functiontool.New(functiontool.Config{Name: "remote_agent"}, func(adkagent.Context, struct{}) (string, error) { return "", nil })
	require.NoError(t, err)
	observer := &inventoryObserver{}
	plugin, err := toolInventoryPlugin(observer, "root", map[string]bool{"remote_agent": true})
	require.NoError(t, err)
	request := &model.LLMRequest{Tools: map[string]any{"read_file": read, "remote_agent": remote, "unresolved": struct{}{}}}
	_, err = plugin.BeforeModelCallback()(inventoryContext{name: "child"}, request)
	require.NoError(t, err)
	require.Empty(t, observer.tools)
	_, err = plugin.BeforeModelCallback()(inventoryContext{name: "root"}, request)
	require.NoError(t, err)
	require.Len(t, observer.tools, 1)
	require.Len(t, observer.tools[0], 1)
	require.Equal(t, "read_file", observer.tools[0][0].Name)
	require.Equal(t, "Read a file", observer.tools[0][0].Description)
	_, err = plugin.BeforeModelCallback()(inventoryContext{name: "root"}, &model.LLMRequest{})
	require.NoError(t, err)
	require.Len(t, observer.tools, 2)
	require.Empty(t, observer.tools[1], "an SDK registry without tools must not acquire defaults")
}
