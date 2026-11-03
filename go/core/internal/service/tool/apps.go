package tool

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func (s *Service) ListMCPAppTools(ctx context.Context, ref MCPServerRef) ([]MCPAppTool, error) {
	ref, err := s.authorizeMCP(ctx, ref, "", apiv1.Permission_PERMISSION_MCP_SERVER_READ)
	if err != nil {
		return nil, err
	}
	if s.mcpClient == nil {
		return nil, serviceerrors.NewFailedPrecondition("MCP client is not configured", nil)
	}
	tools, err := s.mcpClient.ListTools(ctx, ref)
	if err != nil {
		return nil, normalizeMCPError("Failed to list MCP tools", err)
	}
	return tools, nil
}

func (s *Service) CallMCPAppTool(ctx context.Context, ref MCPServerRef, toolName string, arguments any) (*mcp.CallToolResult, error) {
	ref, err := s.authorizeMCP(ctx, ref, toolName, apiv1.Permission_PERMISSION_MCP_TOOL_CALL)
	if err != nil {
		return nil, err
	}
	if s.mcpClient == nil {
		return nil, serviceerrors.NewFailedPrecondition("MCP client is not configured", nil)
	}
	result, err := s.mcpClient.CallTool(ctx, ref, toolName, arguments)
	if err != nil {
		return nil, normalizeMCPError("Failed to call MCP tool", err)
	}
	return result, nil
}

func (s *Service) ReadMCPAppResource(ctx context.Context, ref MCPServerRef, uri string) (*mcp.ReadResourceResult, error) {
	ref, err := s.authorizeMCP(ctx, ref, "", apiv1.Permission_PERMISSION_MCP_SERVER_READ)
	if err != nil {
		return nil, err
	}
	if uri == "" {
		return nil, serviceerrors.NewInvalidArgument("Missing required uri", nil)
	}
	if !strings.HasPrefix(uri, "ui://") {
		return nil, serviceerrors.NewInvalidArgument("MCP Apps resources must use ui:// URIs", nil)
	}
	if s.mcpClient == nil {
		return nil, serviceerrors.NewFailedPrecondition("MCP client is not configured", nil)
	}
	result, err := s.mcpClient.ReadResource(ctx, ref, uri)
	if err != nil {
		return nil, normalizeMCPError("Failed to read MCP resource", err)
	}
	return result, nil
}
