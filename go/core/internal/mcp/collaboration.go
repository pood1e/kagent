package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	runtimev1 "github.com/kagent-dev/kagent/go/api/gen/kagent/runtime/v1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/inventory"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	memoryservice "github.com/kagent-dev/kagent/go/core/internal/service/memory"
)

// Collaboration supplies the same authorized services used by the public API.
type Collaboration struct {
	Agents    *kubecrud.Service[*v1alpha3.Agent, *v1alpha3.AgentList]
	Inventory *inventory.Service
	Memory    *memoryservice.Service
}

func (h *Handler) registerCollaborationTools(server *mcp.Server) error {
	if err := addProtoTool(server, &mcp.Tool{
		Name: "create_session", Description: "Create an Agent conversation. Reuse request_id and input when retrying. Runtime callers inherit their native execution authority.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: new(false)},
	}, func() *apiv1.CreateSessionRequest { return &apiv1.CreateSessionRequest{} }, func() *apiv1.CreateSessionResponse { return &apiv1.CreateSessionResponse{} }, h.createSession); err != nil {
		return err
	}

	if h.collaboration == nil {
		return nil
	}
	if h.collaboration.Agents != nil {
		if err := addProtoTool(server, &mcp.Tool{Name: "list_agents", Description: "List authorized Agent definitions and their runtime-observed tool catalogs.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func() *apiv1.ListAgentsRequest { return &apiv1.ListAgentsRequest{} }, func() *apiv1.ListAgentsResponse { return &apiv1.ListAgentsResponse{} }, h.listAgents); err != nil {
			return err
		}
	}
	if h.collaboration.Memory != nil {
		return addProtoTool(server, &mcp.Tool{Name: "search_memory", Description: "Search authorized personal or team Memory using a text query. The control plane computes embeddings.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func() *runtimev1.MemoryServiceSearchRequest { return &runtimev1.MemoryServiceSearchRequest{} }, func() *runtimev1.MemoryServiceSearchResponse { return &runtimev1.MemoryServiceSearchResponse{} }, h.searchMemory)
	}
	return nil
}

func (h *Handler) createSession(ctx context.Context, _ *mcp.CallToolRequest, request *apiv1.CreateSessionRequest) (*mcp.CallToolResult, *apiv1.CreateSessionResponse, error) {
	session, err := h.sessions.Create(ctx, request.Agent, request.RequestId, request.Name)
	if err != nil {
		return toolError(err), &apiv1.CreateSessionResponse{}, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: session.Id}}}, &apiv1.CreateSessionResponse{Session: session}, nil
}

func (h *Handler) listAgents(ctx context.Context, _ *mcp.CallToolRequest, request *apiv1.ListAgentsRequest) (*mcp.CallToolResult, *apiv1.ListAgentsResponse, error) {
	items, err := h.collaboration.Agents.List(ctx, request.Namespace)
	if err != nil {
		return toolError(err), &apiv1.ListAgentsResponse{}, nil
	}
	limit := int(request.GetPage().GetLimit())
	if limit == 0 {
		limit = 50
	}
	items, next, err := kubecrud.Page(items, limit, request.GetPage().GetPageToken())
	if err != nil {
		return toolError(err), &apiv1.ListAgentsResponse{}, nil
	}
	output := &apiv1.ListAgentsResponse{Page: &apiv1.PageResponse{NextPageToken: next}}
	for _, item := range items {
		resource, err := structuredobject.FromGo(item, v1alpha3.GroupVersion.String(), "Agent", 4<<20)
		if err != nil {
			return toolError(err), &apiv1.ListAgentsResponse{}, nil
		}
		agent := &apiv1.Agent{Ref: &apiv1.ResourceReference{Namespace: item.Namespace, Name: item.Name}, Resource: resource}
		if h.collaboration.Inventory != nil {
			agent.BuiltinTools, err = h.collaboration.Inventory.Lookup(ctx, item.Status.LatestSuccessfulRevision)
			if err != nil {
				return toolError(err), &apiv1.ListAgentsResponse{}, nil
			}
		}
		output.Agents = append(output.Agents, agent)
	}
	return &mcp.CallToolResult{}, output, nil
}

func (h *Handler) searchMemory(ctx context.Context, _ *mcp.CallToolRequest, request *runtimev1.MemoryServiceSearchRequest) (*mcp.CallToolResult, *runtimev1.MemoryServiceSearchResponse, error) {
	output, err := h.collaboration.Memory.SearchProto(ctx, request)
	if err != nil {
		return toolError(err), &runtimev1.MemoryServiceSearchResponse{}, nil
	}
	return &mcp.CallToolResult{}, output, nil
}
