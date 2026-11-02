package authorization

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

func TestResolverUsesResourceAndNamespaceUIDs(t *testing.T) {
	kube := testResources(t)
	r := resolver{kube: kube, store: &testStore{}}
	original, err := r.resolve(t.Context(), agentReference())
	require.NoError(t, err)
	require.Equal(t, "agent:workspace-1/agent-1", original.object)
	require.Equal(t, "agent-1", original.scope.GetKubernetes().Uid)
	agent := &v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Name: "assistant", Namespace: "team"}}
	require.NoError(t, kube.Delete(t.Context(), agent))
	agent.UID = "agent-2"
	require.NoError(t, kube.Create(t.Context(), agent))
	current, err := r.resolve(t.Context(), agentReference())
	require.NoError(t, err)
	require.NotEqual(t, original.object, current.object)
	_, err = r.resolve(t.Context(), original.scope)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound))
}

func TestSessionAuthorityOwnsReadIdentity(t *testing.T) {
	kube := testResources(t)
	session := &apiv1.Session{
		Id: testSessionID, Creator: "actor-storage-owner",
		Agent:     &apiv1.AgentProvenance{Ref: &apiv1.ResourceReference{Namespace: "team", Name: "assistant"}},
		Authority: &apiv1.SessionAuthority{SessionId: testSessionID, InitiatorId: "alice", WorkspaceUid: "workspace-1"},
	}
	store := &testStore{sessions: map[string]*apiv1.Session{testSessionID: session}, tasks: map[string]string{"task-1": testSessionID}}
	r := resolver{kube: kube, store: store}
	resource, err := r.resolve(t.Context(), &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_SessionId{SessionId: testSessionID}})
	require.NoError(t, err)
	owner, err := principalSubject("alice")
	require.NoError(t, err)
	require.Equal(t, owner, resource.facts[1].User)

	task := &apiv1.AuthorizationResource{Resource: &apiv1.AuthorizationResource_Task{Task: &apiv1.TaskReference{
		SessionId: testSessionID, TaskId: "task-1",
	}}}
	resolved, err := r.resolve(t.Context(), task)
	require.NoError(t, err)
	require.Equal(t, resource.object, resolved.facts[2].User)
	require.Equal(t, resource.workspace, resolved.workspace)
	task.GetTask().SessionId = "01990000-0000-7000-8000-000000000002"
	_, err = r.resolve(t.Context(), task)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound))

	session.Authority.WorkspaceUid = ""
	_, err = r.session(t.Context(), testSessionID)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeFailedPrecondition))
	session.Authority.WorkspaceUid = "workspace-1"
	require.NoError(t, kube.Delete(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}}))
	require.NoError(t, kube.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "workspace-2"}}))
	_, err = r.session(t.Context(), testSessionID)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound))
}

func TestResolverRejectsUntrustedObjectIdentity(t *testing.T) {
	r := resolver{kube: testResources(t), store: &testStore{}}
	for _, scope := range []*apiv1.AuthorizationResource{
		{},
		{Resource: &apiv1.AuthorizationResource_Workspace{Workspace: &apiv1.WorkspaceAuthorizationReference{Namespace: "../team"}}},
		{Resource: &apiv1.AuthorizationResource_Kubernetes{Kubernetes: &apiv1.KubernetesAuthorizationReference{
			Kind: sessionScope, Ref: &apiv1.ResourceReference{Namespace: "team", Name: "assistant"},
		}}},
	} {
		_, err := r.resolve(t.Context(), scope)
		require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeInvalidArgument))
	}
	workspace := new(corev1.Namespace)
	require.NoError(t, r.kube.Get(t.Context(), client.ObjectKey{Name: "team"}, workspace))
	wrong := workspaceReference()
	wrong.GetWorkspace().Uid = "wrong"
	_, err := r.resolve(t.Context(), wrong)
	require.True(t, serviceerrors.IsCode(err, serviceerrors.CodeNotFound))
}
