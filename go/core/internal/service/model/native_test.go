package model_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/model"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type modelUIDClient struct{ client.Client }

func (c modelUIDClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if _, ok := object.(*v1alpha3.ModelConfig); ok && object.GetUID() == "" {
		object.SetUID(types.UID(object.GetName() + "-uid"))
	}
	return c.Client.Create(ctx, object, options...)
}

type allowModelPermissions struct{}

func (allowModelPermissions) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	response := new(apiv1.CheckPermissionsResponse)
	for _, check := range request.Checks {
		resource := proto.CloneOf(check.Resource)
		if workspace := resource.GetWorkspace(); workspace != nil {
			workspace.Uid = "workspace-" + workspace.Namespace
		}
		if identity := resource.GetKubernetes(); identity != nil && identity.Uid == "" {
			identity.Uid = identity.Ref.Name + "-uid"
		}
		response.Resources = append(response.Resources, &apiv1.ResourceAccess{Resource: resource, AllowedActions: check.Permissions})
	}
	return response, nil
}

func newModelService(t *testing.T, kube client.Client, authorizer auth.CollectionAuthorizer, namespace string, options ...model.ServiceOption) *model.Service {
	t.Helper()
	if kube != nil {
		list := &v1alpha3.ModelConfigList{}
		if err := kube.List(t.Context(), list); err == nil {
			for i := range list.Items {
				if list.Items[i].UID == "" {
					list.Items[i].UID = types.UID(list.Items[i].Name + "-uid")
					require.NoError(t, kube.Update(t.Context(), &list.Items[i]))
				}
			}
		}
		kube = modelUIDClient{Client: kube}
	}
	return model.NewService(kube, authorizer, allowModelPermissions{}, namespace, options...)
}
