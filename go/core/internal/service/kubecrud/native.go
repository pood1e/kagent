package kubecrud

import (
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/resourceaccess"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// NativeAccess binds Kubernetes CRUD operations to one native resource contract.
type NativeAccess struct {
	Checker resourceaccess.Checker
	Kind    apiv1.AuthorizationScopeKind
	Read    apiv1.Permission
	Create  apiv1.Permission
	Update  apiv1.Permission
	Delete  apiv1.Permission
}

func (a NativeAccess) permission(verb auth.Verb) apiv1.Permission {
	switch verb {
	case auth.VerbGet, auth.VerbList:
		return a.Read
	case auth.VerbCreate:
		return a.Create
	case auth.VerbUpdate:
		return a.Update
	case auth.VerbDelete:
		return a.Delete
	default:
		return apiv1.Permission_PERMISSION_UNSPECIFIED
	}
}

func (a NativeAccess) authorize(ctx context.Context, verb auth.Verb, ref types.NamespacedName) (types.UID, error) {
	permission := a.permission(verb)
	if permission == apiv1.Permission_PERMISSION_UNSPECIFIED {
		return "", serviceerrors.NewUnavailable("Resource permission contract is incomplete", nil)
	}
	if verb == auth.VerbCreate {
		bound, err := resourceaccess.Require(ctx, a.Checker, ref.Namespace, workspaceResource(ref.Namespace), permission)
		if err != nil {
			return "", err
		}
		workspace := bound.GetWorkspace()
		if workspace.GetNamespace() != ref.Namespace || workspace.GetUid() == "" {
			return "", serviceerrors.NewUnavailable("Invalid authorized workspace identity", nil)
		}
		return "", nil
	}
	bound, err := resourceaccess.Require(ctx, a.Checker, ref.Namespace, kubernetesResource(a.Kind, ref, ""), permission)
	if err != nil {
		return "", err
	}
	identity := bound.GetKubernetes()
	if identity.GetKind() != a.Kind || identity.GetRef().GetNamespace() != ref.Namespace || identity.GetRef().GetName() != ref.Name || identity.GetUid() == "" {
		return "", serviceerrors.NewUnavailable("Invalid authorized resource identity", nil)
	}
	return types.UID(identity.Uid), nil
}

func (s *Service[T, L]) filterNative(ctx context.Context, items []T) ([]T, error) {
	if s.native.Read == apiv1.Permission_PERMISSION_UNSPECIFIED {
		return nil, serviceerrors.NewUnavailable("Resource read permission is not configured", nil)
	}
	groups := make(map[string][]int)
	for i, object := range items {
		if object.GetUID() == "" {
			return nil, serviceerrors.NewUnavailable("Resource identity is missing", nil)
		}
		groups[object.GetNamespace()] = append(groups[object.GetNamespace()], i)
	}
	namespaces := make([]string, 0, len(groups))
	for namespace := range groups {
		namespaces = append(namespaces, namespace)
	}
	slices.Sort(namespaces)
	allowed := make([]bool, len(items))
	for _, namespace := range namespaces {
		indices := groups[namespace]
		checks := make([]*apiv1.ResourceGrant, 0, len(indices))
		for _, index := range indices {
			object := items[index]
			checks = append(checks, &apiv1.ResourceGrant{Resource: kubernetesResource(s.native.Kind,
				types.NamespacedName{Namespace: object.GetNamespace(), Name: object.GetName()}, object.GetUID()),
				Permissions: []apiv1.Permission{s.native.Read}})
		}
		access, err := resourceaccess.Check(ctx, s.native.Checker, namespace, checks)
		if err != nil {
			return nil, err
		}
		for i, result := range access {
			if !slices.Contains(result.AllowedActions, s.native.Read) {
				continue
			}
			identity := result.Resource.GetKubernetes()
			object := items[indices[i]]
			if identity.GetKind() != s.native.Kind || identity.GetRef().GetNamespace() != namespace || identity.GetRef().GetName() != object.GetName() || identity.GetUid() != string(object.GetUID()) {
				return nil, serviceerrors.NewUnavailable("Invalid authorized resource identity", nil)
			}
			allowed[indices[i]] = true
		}
	}
	filtered := make([]T, 0, len(items))
	for i, object := range items {
		if allowed[i] {
			filtered = append(filtered, object)
		}
	}
	return filtered, nil
}

func kubernetesResource(kind apiv1.AuthorizationScopeKind, ref types.NamespacedName, uid types.UID) *apiv1.AuthorizationResource {
	return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Kubernetes{
		Kubernetes: &apiv1.KubernetesAuthorizationReference{Kind: kind,
			Ref: &apiv1.ResourceReference{Namespace: ref.Namespace, Name: ref.Name}, Uid: string(uid)},
	}}
}

func workspaceResource(namespace string) *apiv1.AuthorizationResource {
	return &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Workspace{
		Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: namespace},
	}}
}
