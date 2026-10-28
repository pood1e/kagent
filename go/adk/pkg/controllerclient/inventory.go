package controllerclient

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/pkg/logging"
)

type inventoryState struct {
	mu          sync.Mutex
	fingerprint [sha256.Size]byte
	reported    bool
	retryAt     time.Time
}

// ObserveTools publishes a bounded SDK observation after the Actor's snapshot
// restore. An unavailable inventory never interrupts native execution.
func (client *Client) ObserveTools(ctx context.Context, tools []*apiv1.DiscoveredTool) {
	request := &runtimev1.ToolInventoryServiceReportRequest{Tools: tools}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return
	}
	fingerprint := sha256.Sum256(data)
	state := &client.inventory
	state.mu.Lock()
	if state.fingerprint == fingerprint && (state.reported || time.Now().Before(state.retryAt)) {
		state.mu.Unlock()
		return
	}
	state.fingerprint, state.reported, state.retryAt = fingerprint, false, time.Now().Add(time.Minute)
	state.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ctx, cancelCall := client.CallContext(ctx, "")
	defer cancelCall()
	_, err = runtimev1.NewToolInventoryServiceClient(client.connection).Report(ctx, request)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "runtime tool inventory unavailable", "error", err)
		return
	}
	state.mu.Lock()
	if state.fingerprint == fingerprint {
		state.reported = true
	}
	state.mu.Unlock()
}
