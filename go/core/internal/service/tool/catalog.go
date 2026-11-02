package tool

import (
	"context"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

type catalogServer struct {
	database.ToolServer
	tools map[string]string
}

func (s *Service) ListTools(ctx context.Context) ([]database.Tool, error) {
	servers, err := s.visibleServers(ctx)
	if err != nil {
		return nil, err
	}
	return s.visibleTools(ctx, servers)
}

func (s *Service) ListToolServers(ctx context.Context) ([]ToolServer, error) {
	servers, err := s.visibleServers(ctx)
	if err != nil {
		return nil, err
	}
	tools, err := s.visibleTools(ctx, servers)
	if err != nil {
		return nil, err
	}
	byServer := make(map[string][]*v1alpha3.MCPTool)
	for _, tool := range tools {
		byServer[tool.ServerName] = append(byServer[tool.ServerName], &v1alpha3.MCPTool{Name: tool.ID, Description: tool.Description})
	}
	result := make([]ToolServer, 0, len(servers))
	for _, server := range servers {
		discovered := byServer[server.Name]
		if discovered == nil {
			discovered = []*v1alpha3.MCPTool{}
		}
		result = append(result, ToolServer{Ref: server.Name, GroupKind: server.GroupKind, DiscoveredTools: discovered})
	}
	return result, nil
}

func (s *Service) visibleTools(ctx context.Context, servers []catalogServer) ([]database.Tool, error) {
	result := make([]database.Tool, 0)
	if len(servers) == 0 {
		return result, nil
	}
	tools, err := s.discoveryStore.ListTools(ctx)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to list MCP tools", err)
	}
	visible := make(map[string]map[string]string, len(servers))
	for _, server := range servers {
		visible[server.Name] = server.tools
	}
	for _, tool := range tools {
		if tool.GroupKind != remoteMCPServerGVK.GroupKind().String() {
			continue
		}
		if description, ok := visible[tool.ServerName][tool.ID]; ok {
			tool.Description = description
			result = append(result, tool)
		}
	}
	return result, nil
}

func (s *Service) visibleServers(ctx context.Context) ([]catalogServer, error) {
	if err := requireSession(ctx); err != nil {
		return nil, err
	}
	if s.authorizer == nil {
		return nil, serviceerrors.NewUnavailable("Resource authorization is not configured", nil)
	}
	servers, err := s.discoveryStore.ListToolServers(ctx)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to list MCP servers", err)
	}
	checks := make(map[string][]*apiv1.ResourceGrant)
	indices := make(map[string][]int)
	for i, server := range servers {
		namespace, name, ok := strings.Cut(server.Name, "/")
		if !ok || len(utilvalidation.IsDNS1123Label(namespace)) != 0 || len(utilvalidation.IsDNS1123Subdomain(name)) != 0 || server.GroupKind != remoteMCPServerGVK.GroupKind().String() {
			continue
		}
		ref := MCPServerRef{Ref: types.NamespacedName{Namespace: namespace, Name: name}, GroupKind: server.GroupKind}
		checks[namespace] = append(checks[namespace], &apiv1.ResourceGrant{
			Resource: serverResource(ref), Permissions: []apiv1.Permission{apiv1.Permission_PERMISSION_MCP_SERVER_READ},
		})
		indices[namespace] = append(indices[namespace], i)
	}
	namespaces := make([]string, 0, len(checks))
	for namespace := range checks {
		namespaces = append(namespaces, namespace)
	}
	slices.Sort(namespaces)
	visible := make(map[int]catalogServer)
	for _, namespace := range namespaces {
		access, err := s.checkPermissions(ctx, namespace, checks[namespace])
		if err != nil {
			return nil, err
		}
		for i, resource := range access {
			if !slices.Contains(resource.AllowedActions, apiv1.Permission_PERMISSION_MCP_SERVER_READ) {
				continue
			}
			bound := resource.Resource.GetKubernetes()
			requested := checks[namespace][i].Resource.GetKubernetes().Ref
			if bound.GetKind() != apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MCP_SERVER || bound.GetUid() == "" || bound.GetRef().GetNamespace() != namespace || bound.GetRef().GetName() != requested.Name {
				return nil, serviceerrors.NewUnavailable("Invalid authorized MCP server identity", nil)
			}
			current := new(v1alpha3.RemoteMCPServer)
			if err := s.kubeClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: requested.Name}, current); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, serviceerrors.NewUnavailable("Failed to verify MCP server identity", err)
			}
			if string(current.UID) != bound.Uid {
				continue
			}
			index := indices[namespace][i]
			entry := catalogServer{ToolServer: servers[index], tools: make(map[string]string)}
			if current.Generation == current.Status.ObservedGeneration {
				for _, tool := range current.Status.DiscoveredTools {
					if tool != nil {
						entry.tools[tool.Name] = tool.Description
					}
				}
			}
			visible[index] = entry
		}
	}
	result := make([]catalogServer, 0, len(visible))
	for i := range servers {
		if server, ok := visible[i]; ok {
			result = append(result, server)
		}
	}
	return result, nil
}
