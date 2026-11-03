package tool

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/resourceaccess"
	"github.com/kagent-dev/kagent/go/core/internal/service/secretmaterial"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/internal/utils"
	"github.com/kagent-dev/kagent/go/pkg/logging"
)

type ServerType string

const ServerTypeRemoteMCPServer ServerType = "RemoteMCPServer"

var remoteMCPServerGVK = v1alpha3.GroupVersion.WithKind(string(ServerTypeRemoteMCPServer))

type DiscoveryStore interface {
	ListTools(context.Context) ([]database.Tool, error)
	ListToolServers(context.Context) ([]database.ToolServer, error)
}

type MCPClient interface {
	ListTools(context.Context, MCPServerRef) ([]MCPAppTool, error)
	CallTool(context.Context, MCPServerRef, string, any) (*mcp.CallToolResult, error)
	ReadResource(context.Context, MCPServerRef, string) (*mcp.ReadResourceResult, error)
}

type Service struct {
	kubeClient       client.Client
	discoveryStore   DiscoveryStore
	authorizer       resourceaccess.Checker
	defaultNamespace string
	mcpClient        MCPClient
}

type ToolServer struct {
	Ref             string
	GroupKind       string
	DiscoveredTools []*v1alpha3.MCPTool
}

type CreateToolServerRequest struct {
	Type            ServerType
	RemoteMCPServer *v1alpha3.RemoteMCPServer
	Secrets         []secretmaterial.Material
}

type MCPServerRef struct {
	Ref       types.NamespacedName
	GroupKind string
	UID       types.UID
}

type MCPAppTool struct {
	Name          string
	Description   string
	InputSchema   any
	UIResourceURI string
	Meta          map[string]any
}

func NewService(
	kubeClient client.Client,
	discoveryStore DiscoveryStore,
	authorizer resourceaccess.Checker,
	defaultNamespace string,
	mcpClient MCPClient,
) *Service {
	if mcpClient == nil && kubeClient != nil {
		mcpClient = NewRuntimeMCPClient(kubeClient)
	}
	return &Service{
		kubeClient:       kubeClient,
		discoveryStore:   discoveryStore,
		authorizer:       authorizer,
		defaultNamespace: defaultNamespace,
		mcpClient:        mcpClient,
	}
}

func (s *Service) CreateToolServer(ctx context.Context, request CreateToolServerRequest) (client.Object, error) {
	supportedTypes := s.supportedServerTypes()
	if !slices.Contains(supportedTypes, request.Type) {
		return nil, serviceerrors.NewInvalidArgument(
			fmt.Sprintf("Invalid tool server type. Must be one of %s", joinServerTypes(supportedTypes)),
			nil,
		)
	}

	var owner client.Object
	var gvk schema.GroupVersionKind
	switch request.Type {
	case ServerTypeRemoteMCPServer:
		if request.RemoteMCPServer == nil {
			return nil, serviceerrors.NewInvalidArgument("RemoteMCPServer data is required when type is RemoteMCPServer", nil)
		}
		owner = request.RemoteMCPServer
		gvk = remoteMCPServerGVK
	}

	ref, err := normalizeCreateRef(owner, s.defaultNamespace)
	if err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid ToolServer metadata", err)
	}
	bound, err := s.requirePermission(ctx, ref.Namespace, workspaceResource(ref.Namespace), apiv1.Permission_PERMISSION_MCP_SERVER_MANAGE)
	if err != nil {
		return nil, err
	}
	if bound.GetWorkspace().GetNamespace() != ref.Namespace || bound.GetWorkspace().GetUid() == "" {
		return nil, serviceerrors.NewUnavailable("Invalid authorized workspace identity", nil)
	}
	if err := secretmaterial.ValidateMaterials(request.Secrets); err != nil {
		return nil, err
	}

	if err := s.kubeClient.Create(ctx, owner); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, serviceerrors.NewAlreadyExists(string(request.Type)+" already exists", err)
		}
		return nil, serviceerrors.NewInternal("Failed to create "+string(request.Type)+" in Kubernetes", err)
	}
	if err := secretmaterial.CreateCompanionSecrets(ctx, s.kubeClient, owner, gvk, request.Secrets); err != nil {
		if rollbackErr := secretmaterial.RollbackOwnerOnCreateFailure(ctx, s.kubeClient, owner); rollbackErr != nil {
			logging.FromContext(ctx).ErrorContext(ctx, "failed to roll back tool server after companion secret failure", "error", rollbackErr)
		}
		return nil, err
	}
	return owner, nil
}

func (s *Service) DeleteToolServer(ctx context.Context, ref types.NamespacedName) error {
	bound, err := s.authorizeMCP(ctx, MCPServerRef{Ref: ref, GroupKind: remoteMCPServerGVK.GroupKind().String()}, "", apiv1.Permission_PERMISSION_MCP_SERVER_MANAGE)
	if err != nil {
		return err
	}
	object := new(v1alpha3.RemoteMCPServer)
	if err := s.kubeClient.Get(ctx, ref, object); err != nil {
		if apierrors.IsNotFound(err) {
			return serviceerrors.NewNotFound("RemoteMCPServer not found", err)
		}
		return serviceerrors.NewInternal("Failed to get RemoteMCPServer", err)
	}
	if object.UID != bound.UID {
		return serviceerrors.NewPermissionDenied("MCP server identity changed", nil)
	}
	if err := s.kubeClient.Delete(ctx, object, client.Preconditions{UID: &object.UID, ResourceVersion: &object.ResourceVersion}); err != nil {
		if apierrors.IsConflict(err) {
			return serviceerrors.NewFailedPrecondition("MCP server changed before deletion", err)
		}
		return serviceerrors.NewInternal("Failed to delete RemoteMCPServer", err)
	}
	return nil
}

func (s *Service) ListToolServerTypes(ctx context.Context) ([]ServerType, error) {
	if err := requireSession(ctx); err != nil {
		return nil, err
	}
	return s.supportedServerTypes(), nil
}

func (s *Service) supportedServerTypes() []ServerType { return []ServerType{ServerTypeRemoteMCPServer} }

func normalizeCreateRef(object client.Object, defaultNamespace string) (types.NamespacedName, error) {
	namespace := object.GetNamespace()
	if namespace == "" {
		namespace = defaultNamespace
	}
	ref, err := utils.ParseRefString(object.GetName(), namespace)
	if err != nil {
		return types.NamespacedName{}, err
	}
	object.SetNamespace(ref.Namespace)
	object.SetName(ref.Name)
	return ref, nil
}

func joinServerTypes(serverTypes []ServerType) string {
	values := make([]string, 0, len(serverTypes))
	for _, serverType := range serverTypes {
		values = append(values, string(serverType))
	}
	return strings.Join(values, ", ")
}

func normalizeMCPError(message string, err error) error {
	if serviceerrors.CodeOf(err) != "" {
		return err
	}
	return serviceerrors.NewInternal(message, err)
}
