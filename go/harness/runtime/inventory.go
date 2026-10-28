package runtime

import (
	"context"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// ToolObserver receives the vendor's effective tool catalog, when introspection
// is available. Absence remains unknown rather than an observed empty catalog.
type ToolObserver interface {
	ObserveTools(context.Context, []*apiv1.DiscoveredTool)
}
