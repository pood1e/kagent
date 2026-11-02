package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"buf.build/go/protovalidate"
	fga "github.com/openfga/go-sdk"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

type resourceStore interface {
	GetSessionByID(context.Context, string) (*apiv1.Session, error)
	SessionForTask(context.Context, string) (string, error)
}

type resolvedResource struct {
	scope     *apiv1.AuthorizationResource
	kind      apiv1.AuthorizationScopeKind
	object    string
	workspace string
	namespace string
	facts     []fga.TupleKey
}

type resolver struct {
	kube  client.Reader
	store resourceStore
}

func (r *resolver) resolve(ctx context.Context, scope *apiv1.AuthorizationResource) (*resolvedResource, error) {
	if err := protovalidate.Validate(scope); err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid authorization resource", err)
	}
	switch ref := scope.Resource.(type) {
	case *apiv1.AuthorizationResource_Workspace:
		workspace, err := r.workspace(ctx, ref.Workspace.Namespace, ref.Workspace.Uid)
		if err != nil {
			return nil, err
		}
		scope := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
			Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: workspace.Name, Uid: string(workspace.UID)},
		}}
		key := "workspace:" + string(workspace.UID)
		return &resolvedResource{scope: scope, kind: workspaceScope, object: key, workspace: key, namespace: workspace.Name}, nil
	case *apiv1.AuthorizationResource_Kubernetes:
		return r.kubernetes(ctx, ref.Kubernetes)
	case *apiv1.AuthorizationResource_McpTool:
		if ref.McpTool.Server.Kind != mcpServerScope {
			return nil, serviceerrors.NewInvalidArgument("MCP tool requires an MCP server", nil)
		}
		server, err := r.kubernetes(ctx, ref.McpTool.Server)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256([]byte(server.object + "\x00" + ref.McpTool.Name))
		key := "mcp_tool:" + hex.EncodeToString(hash[:])
		return &resolvedResource{
			scope: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_McpTool{
				McpTool: &apiv1.MCPToolAuthorizationReference{Server: proto.CloneOf(server.scope.GetKubernetes()), Name: ref.McpTool.Name},
			}},
			kind: mcpToolScope, object: key, workspace: server.workspace, namespace: server.namespace,
			facts: append(server.facts, fga.TupleKey{User: server.object, Relation: "server", Object: key}),
		}, nil
	case *apiv1.AuthorizationResource_SessionId:
		return r.session(ctx, ref.SessionId)
	case *apiv1.AuthorizationResource_Task:
		sessionID, err := r.store.SessionForTask(ctx, ref.Task.TaskId)
		if err != nil {
			return nil, resolveError(err)
		}
		if sessionID != ref.Task.SessionId {
			return nil, serviceerrors.NewNotFound("Authorization resource not found", nil)
		}
		parent, err := r.session(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(ref.Task)
		if err != nil {
			return nil, serviceerrors.NewInvalidArgument("Invalid task reference", err)
		}
		hash := sha256.Sum256(encoded)
		key := "task:" + hex.EncodeToString(hash[:])
		return &resolvedResource{
			scope: proto.CloneOf(scope), kind: taskScope, object: key, workspace: parent.workspace, namespace: parent.namespace,
			facts: append(parent.facts, fga.TupleKey{User: parent.object, Relation: "session", Object: key}),
		}, nil
	default:
		return nil, serviceerrors.NewInvalidArgument("Authorization resource is required", nil)
	}
}

func (r *resolver) workspace(ctx context.Context, namespace, expected string) (*corev1.Namespace, error) {
	workspace := new(corev1.Namespace)
	if err := r.kube.Get(ctx, types.NamespacedName{Name: namespace}, workspace); err != nil {
		return nil, resolveError(err)
	}
	if workspace.UID == "" || expected != "" && expected != string(workspace.UID) {
		return nil, serviceerrors.NewNotFound("Authorization resource not found", nil)
	}
	return workspace, nil
}

func (r *resolver) kubernetes(ctx context.Context, ref *apiv1.KubernetesAuthorizationReference) (*resolvedResource, error) {
	workspace, err := r.workspace(ctx, ref.Ref.Namespace, "")
	if err != nil {
		return nil, err
	}
	var object client.Object
	var objectType string
	switch ref.Kind {
	case agentScope:
		object, objectType = new(v1alpha3.Agent), "agent"
	case templateScope:
		object, objectType = new(v1alpha3.AgentTemplate), "agent_template"
	case harnessScope:
		object, objectType = new(v1alpha3.Harness), "harness"
	case modelConnectionScope:
		object, objectType = new(v1alpha3.ModelConfig), "model_connection"
	case mcpServerScope:
		object, objectType = new(v1alpha3.RemoteMCPServer), "mcp_server"
	default:
		return nil, serviceerrors.NewInvalidArgument("Unsupported Kubernetes authorization kind", nil)
	}
	if err := r.kube.Get(ctx, types.NamespacedName{Namespace: ref.Ref.Namespace, Name: ref.Ref.Name}, object); err != nil {
		return nil, resolveError(err)
	}
	if object.GetUID() == "" || ref.Uid != "" && ref.Uid != string(object.GetUID()) {
		return nil, serviceerrors.NewNotFound("Authorization resource not found", nil)
	}
	scope := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
		Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: ref.Kind, Ref: proto.CloneOf(ref.Ref), Uid: string(object.GetUID())},
	}}
	key := objectType + ":" + string(workspace.UID) + "/" + string(object.GetUID())
	workspaceKey := "workspace:" + string(workspace.UID)
	return &resolvedResource{
		scope: scope, kind: ref.Kind, object: key, workspace: workspaceKey, namespace: workspace.Name,
		facts: []fga.TupleKey{{User: workspaceKey, Relation: "workspace", Object: key}},
	}, nil
}

func (r *resolver) session(ctx context.Context, id string) (*resolvedResource, error) {
	session, err := r.store.GetSessionByID(ctx, id)
	if err != nil {
		return nil, resolveError(err)
	}
	authority := session.GetAuthority()
	if authority.GetWorkspaceUid() == "" || authority.GetInitiatorId() == "" {
		return nil, serviceerrors.NewFailedPrecondition("Session requires a recorded workspace and initiator identity", nil)
	}
	if authority.SessionId != id {
		return nil, serviceerrors.NewFailedPrecondition("Session authority identity mismatch", nil)
	}
	workspace, err := r.workspace(ctx, session.GetAgent().GetRef().GetNamespace(), authority.WorkspaceUid)
	if err != nil {
		return nil, err
	}
	owner, err := principalSubject(authority.InitiatorId)
	if err != nil {
		return nil, serviceerrors.NewFailedPrecondition("Session initiator identity is invalid", err)
	}
	workspaceKey := "workspace:" + string(workspace.UID)
	key := "session:" + string(workspace.UID) + "/" + id
	return &resolvedResource{
		scope: &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_SessionId{SessionId: id}},
		kind:  sessionScope, object: key, workspace: workspaceKey, namespace: workspace.Name,
		facts: []fga.TupleKey{
			{User: workspaceKey, Relation: "workspace", Object: key},
			{User: owner, Relation: "owner", Object: key},
		},
	}, nil
}

func resolveError(err error) error {
	if errors.Is(err, database.ErrNotFound) || apierrors.IsNotFound(err) {
		return serviceerrors.NewNotFound("Authorization resource not found", nil)
	}
	return serviceerrors.NewUnavailable("Failed to resolve authorization resource", fmt.Errorf("read resource identity: %w", err))
}

func matchesWorkspace(resource *resolvedResource, expected *apiv1.WorkspaceAuthorizationReference) bool {
	return expected != nil && resource.namespace == expected.Namespace &&
		(expected.Uid == "" || resource.workspace == "workspace:"+expected.Uid)
}
