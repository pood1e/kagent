package controllerclient

import (
	"context"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	a2agrpc "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	"github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"google.golang.org/grpc/metadata"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// SessionService shares the runtime's authenticated controller connection.
func (client *Client) SessionService() apiv1.SessionServiceClient {
	return apiv1.NewSessionServiceClient(client.connection)
}

// ExecutionService observes control-plane-owned native continuation progress.
func (client *Client) ExecutionService() apiv1.ExecutionServiceClient {
	return apiv1.NewExecutionServiceClient(client.connection)
}

// AgentClient routes native A2A calls through the controller's session gateway.
// Its transport borrows the controller connection and never closes it.
func (client *Client) AgentClient(ctx context.Context, ref *apiv1.ResourceReference) (*a2aclient.Client, error) {
	endpoint := &a2a.AgentInterface{
		URL:             strings.TrimPrefix(client.connection.Target(), "passthrough:///"),
		ProtocolBinding: a2a.TransportProtocolGRPC, ProtocolVersion: a2a.Version,
		Tenant: ref.Namespace + "/" + ref.Name,
	}
	return a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{endpoint}, a2aclient.WithCallInterceptors(agentIdentity{client: client}), a2aclient.WithTransport(
		a2a.TransportProtocolGRPC,
		a2aclient.TransportFactoryFn(func(context.Context, *a2a.AgentCard, *a2a.AgentInterface) (a2aclient.Transport, error) {
			return a2agrpc.NewGRPCTransportFromClient(a2apb.NewA2AServiceClient(client.connection)), nil
		}),
	))
}

type agentIdentity struct{ client *Client }

func (i agentIdentity) Before(ctx context.Context, request *a2aclient.Request) (context.Context, any, error) {
	ctx = i.client.StreamContext(ctx, "")
	values, _ := metadata.FromOutgoingContext(ctx)
	// The SDK renders ServiceParams as a fresh metadata map. Supply identity
	// there so its protocol headers cannot replace the authenticated origin.
	for _, name := range []string{"authorization", "x-agent-name", "x-user-id"} {
		if value := values.Get(name); len(value) > 0 {
			request.ServiceParams[name] = value
		}
	}
	return ctx, nil, nil
}

func (agentIdentity) After(context.Context, *a2aclient.Response) error { return nil }
