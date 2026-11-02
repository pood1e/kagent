package session

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func TestWorkspaceIdentityIsCapturedAndDelegationKeepsIt(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "workspace-1"}},
	).Build()
	service := new(Service)
	WithWorkspaceIdentity(reader)(service)
	root := &apiv1.SessionAuthority{}
	require.NoError(t, service.bindWorkspace(t.Context(), "team", root))
	require.Equal(t, "workspace-1", root.WorkspaceUid)
	for _, uid := range []string{"workspace-1", "workspace-2", ""} {
		grant := &apiv1.SessionAuthority{ParentSessionId: "parent", WorkspaceUid: uid}
		err := service.bindWorkspace(t.Context(), "team", grant)
		if uid == "workspace-1" {
			require.NoError(t, err)
		} else {
			require.True(t, serviceerrors.IsCode(err, serviceerrors.CodePermissionDenied))
		}
	}
}
