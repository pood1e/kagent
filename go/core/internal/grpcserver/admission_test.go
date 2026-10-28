package grpcserver

import (
	"testing"

	"github.com/stretchr/testify/require"
	atev1alpha1 "go.ate.dev/substrate/pkg/api/v1alpha1"
	"google.golang.org/grpc/codes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/admission"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	modelsvc "github.com/kagent-dev/kagent/go/core/internal/service/model"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func TestNativeAdmissionThroughGeneratedClients(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, atev1alpha1.AddToScheme(scheme))
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	harness := testHarness("team", "codex", "pool")
	model := &v1alpha3.ModelConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "model"}, Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "gpt-test", OpenAI: &v1alpha3.OpenAIConfig{APIFormat: new(v1alpha3.OpenAIAPIFormatResponses)}}}
	model.Spec.APIKeySecret, model.Spec.APIKeySecretKey = "model-auth", "key"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "model-auth"}, Data: map[string][]byte{"key": []byte("test-key")}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(harness, model, secret, &atev1alpha1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "pool"}}).Build()
	admission := admission.NewService(kube, &configv1.RuntimeDefaultsConfig{ControlPlaneUrl: new("http://control:8083")}, nil)
	connection := newConfigurationConnection(t, kube, func(config *Config) {
		config.AgentService = kubecrud.NewService(kube, &auth.NoopAuthorizer{}, &v1alpha3.Agent{}, &v1alpha3.AgentList{}, "Agent", admission.Agent)
		config.ModelService = modelsvc.NewService(kube, &auth.NoopAuthorizer{}, "team", modelsvc.WithAdmission(admission))
	})
	agents := apiv1.NewAgentServiceClient(connection)
	ref := &apiv1.ResourceReference{Namespace: "team", Name: "assistant"}
	candidate := &v1alpha3.Agent{Spec: v1alpha3.AgentSpec{HarnessRef: corev1.LocalObjectReference{Name: "codex"}, Template: v1alpha3.AgentTemplateSource{Inline: &v1alpha3.AgentTemplateSpec{ModelConfig: &corev1.LocalObjectReference{Name: "missing"}, SystemPrompt: "help"}}}}
	resource := func() *apiv1.StructuredObject {
		t.Helper()
		encoded, err := structuredobject.FromGo(candidate, v1alpha3.GroupVersion.String(), "Agent", DefaultMaxMessageSize)
		require.NoError(t, err)
		return encoded
	}
	_, err := agents.ValidateAgent(t.Context(), &apiv1.ValidateAgentRequest{Ref: ref, Resource: resource()})
	assertGRPCCode(t, err, codes.InvalidArgument)
	_, err = agents.CreateAgent(t.Context(), &apiv1.CreateAgentRequest{Ref: ref, Resource: resource()})
	assertGRPCCode(t, err, codes.InvalidArgument)
	list := &v1alpha3.AgentList{}
	require.NoError(t, kube.List(t.Context(), list))
	require.Empty(t, list.Items)
	candidate.Spec.Template.Inline.ModelConfig.Name = model.Name
	_, err = agents.ValidateAgent(t.Context(), &apiv1.ValidateAgentRequest{Ref: ref, Resource: resource()})
	require.NoError(t, err)
	require.NoError(t, kube.List(t.Context(), list))
	require.Empty(t, list.Items, "successful preflight must not persist a resource")
	_, err = agents.CreateAgent(t.Context(), &apiv1.CreateAgentRequest{Ref: ref, Resource: resource()})
	require.NoError(t, err)
	candidate.Spec.HarnessRef.Name = "missing"
	_, err = agents.UpdateAgent(t.Context(), &apiv1.UpdateAgentRequest{Ref: ref, Resource: resource()})
	assertGRPCCode(t, err, codes.InvalidArgument)
	stored := &v1alpha3.Agent{}
	require.NoError(t, kube.Get(t.Context(), types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, stored))
	require.Equal(t, "codex", stored.Spec.HarnessRef.Name)
	badModel := model.DeepCopy()
	badModel.Spec.OpenAI.APIFormat = new(v1alpha3.OpenAIAPIFormatChatCompletions)
	encoded, err := structuredobject.FromGo(badModel, v1alpha3.GroupVersion.String(), "ModelConfig", DefaultMaxMessageSize)
	require.NoError(t, err)
	_, err = apiv1.NewModelServiceClient(connection).UpdateModelConfig(t.Context(), &apiv1.UpdateModelConfigRequest{Ref: &apiv1.ResourceReference{Namespace: "team", Name: model.Name}, Resource: encoded})
	assertGRPCCode(t, err, codes.InvalidArgument)
	require.NoError(t, kube.Get(t.Context(), types.NamespacedName{Namespace: "team", Name: model.Name}, badModel))
	require.Equal(t, v1alpha3.OpenAIAPIFormatResponses, *badModel.Spec.OpenAI.APIFormat)
}
