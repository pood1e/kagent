package kubecrud_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiauthorization "github.com/kagent-dev/kagent/go/api/authorization"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	"github.com/kagent-dev/kagent/go/core/internal/service/resourceaccess"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type nativeChecker struct {
	requests []*apiv1.CheckPermissionsRequest
	allow    func(*apiv1.AuthorizationResource, apiv1.Permission) bool
	uid      string
	broken   bool
}

func (c *nativeChecker) CheckPermissions(_ context.Context, request *apiv1.CheckPermissionsRequest) (*apiv1.CheckPermissionsResponse, error) {
	c.requests = append(c.requests, proto.CloneOf(request))
	response := new(apiv1.CheckPermissionsResponse)
	if c.broken {
		return response, nil
	}
	for _, check := range request.Checks {
		resource := proto.CloneOf(check.Resource)
		if workspace := resource.GetWorkspace(); workspace != nil {
			workspace.Uid = "workspace-uid"
		}
		if identity := resource.GetKubernetes(); identity != nil {
			if identity.Uid == "" {
				identity.Uid = identity.Ref.Name + "-uid"
			}
			if c.uid != "" {
				identity.Uid = c.uid
			}
		}
		access := &apiv1.ResourceAccess{Resource: resource}
		for _, permission := range check.Permissions {
			if c.allow == nil || c.allow(resource, permission) {
				access.AllowedActions = append(access.AllowedActions, permission)
			}
		}
		response.Resources = append(response.Resources, access)
	}
	return response, nil
}

func nativeHarnessService(t *testing.T, checker *nativeChecker, objects ...client.Object) (*kubecrud.Service[*v1alpha3.Harness, *v1alpha3.HarnessList], client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	legacy := &recordingAuthorizer{scope: apiauthorization.AuthorizationScope{Kind: apiauthorization.ScopeAll}}
	var permissions resourceaccess.Checker
	if checker != nil {
		permissions = checker
	}
	service := kubecrud.NewNativeService(kube, legacy, kubecrud.NativeAccess{
		Checker: permissions,
		Kind:    apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_HARNESS,
		Read:    apiv1.Permission_PERMISSION_HARNESS_READ,
		Create:  apiv1.Permission_PERMISSION_HARNESS_MANAGE,
		Update:  apiv1.Permission_PERMISSION_HARNESS_MANAGE,
		Delete:  apiv1.Permission_PERMISSION_HARNESS_MANAGE,
	}, &v1alpha3.Harness{}, &v1alpha3.HarnessList{}, "Harness")
	return service, kube
}

func TestNativeHarnessReadAndManageAreSeparate(t *testing.T) {
	checker := &nativeChecker{allow: func(resource *apiv1.AuthorizationResource, permission apiv1.Permission) bool {
		return resource.GetKubernetes().GetRef().GetName() == "allowed" && permission == apiv1.Permission_PERMISSION_HARNESS_READ
	}}
	service, kube := nativeHarnessService(t, checker,
		&v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "allowed", UID: "allowed-uid"}},
		&v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "denied", UID: "denied-uid"}},
	)
	ctx := auth.AuthSessionTo(t.Context(), testSession{})
	items, err := service.List(ctx, "team")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "allowed", items[0].Name)
	_, err = service.Get(ctx, types.NamespacedName{Namespace: "team", Name: "allowed"})
	require.NoError(t, err)
	_, err = service.Get(ctx, types.NamespacedName{Namespace: "team", Name: "denied"})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	object := items[0].DeepCopy()
	_, err = service.Update(ctx, object)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	err = service.Delete(ctx, types.NamespacedName{Namespace: "team", Name: "allowed"}, "")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	require.NoError(t, kube.Get(ctx, types.NamespacedName{Namespace: "team", Name: "allowed"}, &v1alpha3.Harness{}))
	_, err = service.Create(ctx, &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "created"}})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
	require.Equal(t, apiv1.Permission_PERMISSION_HARNESS_MANAGE, checker.requests[len(checker.requests)-1].Checks[0].Permissions[0])
	require.NotNil(t, checker.requests[len(checker.requests)-1].Checks[0].Resource.GetWorkspace())
}

func TestNativeHarnessFencesAuthorizedUID(t *testing.T) {
	checker := &nativeChecker{uid: "old-uid"}
	service, kube := nativeHarnessService(t, checker, &v1alpha3.Harness{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "runtime", UID: "replacement-uid"},
	})
	ctx := auth.AuthSessionTo(t.Context(), testSession{})
	ref := types.NamespacedName{Namespace: "team", Name: "runtime"}
	_, err := service.Get(ctx, ref)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound))
	_, err = service.Update(ctx, &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "runtime", UID: "replacement-uid"}})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound))
	err = service.Delete(ctx, ref, "")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound))
	require.NoError(t, kube.Get(ctx, ref, &v1alpha3.Harness{}))
}

func TestNativeHarnessListBatchesBeforePublishing(t *testing.T) {
	objects := make([]client.Object, 101)
	for i := range objects {
		name := fmt.Sprintf("runtime-%03d", i)
		objects[i] = &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: name, UID: types.UID(name + "-uid")}}
	}
	checker := &nativeChecker{allow: func(resource *apiv1.AuthorizationResource, _ apiv1.Permission) bool {
		return resource.GetKubernetes().GetRef().GetName() == "runtime-100"
	}}
	service, _ := nativeHarnessService(t, checker, objects...)
	items, err := service.List(auth.AuthSessionTo(t.Context(), testSession{}), "team")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "runtime-100", items[0].Name)
	require.Len(t, checker.requests, 2)
	require.Len(t, checker.requests[0].Checks, 100)
	require.Len(t, checker.requests[1].Checks, 1)
	for _, request := range checker.requests {
		for _, check := range request.Checks {
			require.NotEmpty(t, check.Resource.GetKubernetes().GetUid())
		}
	}
}

func TestNativeHarnessFailsClosedOnMissingOrMalformedEngine(t *testing.T) {
	service, _ := nativeHarnessService(t, nil)
	ctx := auth.AuthSessionTo(t.Context(), testSession{})
	_, err := service.List(ctx, "team")
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
	_, err = service.Create(ctx, &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "runtime"}})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
	service, _ = nativeHarnessService(t, &nativeChecker{broken: true})
	_, err = service.Create(ctx, &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "runtime"}})
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeUnavailable))
}
