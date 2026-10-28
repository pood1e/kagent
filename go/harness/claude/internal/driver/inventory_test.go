package driver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

type inventoryObserver struct{ reports [][]*apiv1.DiscoveredTool }

func (o *inventoryObserver) ObserveTools(_ context.Context, tools []*apiv1.DiscoveredTool) {
	o.reports = append(o.reports, tools)
}

func TestInventoryIsObservedAndExcludesMCPAndDisabledTools(t *testing.T) {
	observer := &inventoryObserver{}
	driver := NewProcessDriver(ProcessConfig{ToolObserver: observer, DisallowedTools: []string{"Bash"}})
	driver.observeTools(t.Context(), nil)
	require.Empty(t, observer.reports)
	names := []string{"Read", "Bash", "mcp__team__query"}
	driver.observeTools(t.Context(), &names)
	require.Equal(t, [][]*apiv1.DiscoveredTool{{{Name: "Read"}}}, observer.reports)
	names = nil
	driver.observeTools(t.Context(), &names)
	require.Len(t, observer.reports, 2, "observed empty is published")
	require.Empty(t, observer.reports[1])
}
