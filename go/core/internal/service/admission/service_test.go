package admission

import (
	"testing"

	"github.com/stretchr/testify/require"
	atev1alpha1 "go.ate.dev/substrate/pkg/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func admissionFixture(t *testing.T, kind string) (*Service, *v1alpha3.Agent, *v1alpha3.ModelConfig) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, atev1alpha1.AddToScheme(scheme))
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	model := &v1alpha3.ModelConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "model"}, Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "gpt-test", OpenAI: &v1alpha3.OpenAIConfig{APIFormat: new(v1alpha3.OpenAIAPIFormatResponses)}}}
	model.Spec.APIKeySecret, model.Spec.APIKeySecretKey = "model-auth", "key"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "model-auth"}, Data: map[string][]byte{"key": []byte("test-key")}}
	harness := &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "harness"}, Spec: v1alpha3.HarnessSpec{Workload: v1alpha3.HarnessWorkload{Image: new("example.com/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}, Substrate: v1alpha3.RuntimeSubstratePolicy{WorkerPoolRef: corev1.LocalObjectReference{Name: "pool"}, SnapshotPolicy: v1alpha3.RuntimeSnapshotPolicy{Location: "snapshots"}}}}
	switch kind {
	case "kagent":
		harness.Spec.Kagent = &v1alpha3.KagentHarness{}
	case "codex":
		harness.Spec.Codex = &v1alpha3.CodexHarness{}
	case "claude":
		harness.Spec.Claude = &v1alpha3.ClaudeHarness{}
		model.Spec.Provider, model.Spec.Model, model.Spec.OpenAI = v1alpha3.ModelProviderAnthropic, "claude-test", nil
	}
	template := &v1alpha3.AgentTemplate{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "template"}, Spec: v1alpha3.AgentTemplateSpec{ModelConfig: &corev1.LocalObjectReference{Name: model.Name}, SystemPrompt: "help"}}
	agent := &v1alpha3.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "assistant"}, Spec: v1alpha3.AgentSpec{HarnessRef: corev1.LocalObjectReference{Name: harness.Name}, Template: v1alpha3.AgentTemplateSource{Ref: &corev1.LocalObjectReference{Name: template.Name}}}}
	pool := &atev1alpha1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "pool"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model, harness, template, agent, pool, secret).Build()
	settings := &configv1.RuntimeDefaultsConfig{ControlPlaneUrl: new("http://control:8083"), Logging: &configv1.LoggingConfig{Level: new("info")}}
	return NewService(kube, settings, nil), agent, model
}

func TestAdmissionReusesEachHarnessCompiler(t *testing.T) {
	for _, kind := range []string{"kagent", "claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			service, agent, model := admissionFixture(t, kind)
			require.NoError(t, service.Agent(t.Context(), agent))
			candidate := model.DeepCopy()
			candidate.Spec.Model = ""
			require.Error(t, service.ValidateModel(t.Context(), candidate, nil))
			stored := &v1alpha3.ModelConfig{}
			require.NoError(t, service.kube.Get(t.Context(), client.ObjectKeyFromObject(model), stored))
			require.Equal(t, model.Spec.Model, stored.Spec.Model, "compilation must be read-only")
			if kind != "kagent" {
				candidate = model.DeepCopy()
				candidate.Spec.TokenPassthrough = new(true)
				require.ErrorContains(t, service.ValidateModel(t.Context(), candidate, nil), "tokenPassthrough")
			}
		})
	}
}

func TestAdmissionResolvesPendingCredentialsWithoutApplyingThem(t *testing.T) {
	service, _, model := admissionFixture(t, "codex")
	model.Spec.APIKeySecret, model.Spec.APIKeySecretKey = "pending", "key"
	require.ErrorContains(t, service.ValidateModel(t.Context(), model, nil), "not found")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: model.Namespace, Name: "pending"}, Data: map[string][]byte{"key": []byte("not-returned-or-persisted")}}
	require.NoError(t, service.ValidateModel(t.Context(), model, []*corev1.Secret{secret}))
	require.Error(t, service.kube.Get(t.Context(), client.ObjectKeyFromObject(secret), &corev1.Secret{}))
}

func TestAdmissionChecksOnlyTransitiveDependents(t *testing.T) {
	service, agent, model := admissionFixture(t, "codex")
	unrelated := agent.DeepCopy()
	unrelated.ResourceVersion = ""
	unrelated.Name, unrelated.Spec.HarnessRef.Name = "broken-unrelated", "missing"
	unrelated.Spec.Template.Ref.Name = "unrelated"
	require.NoError(t, service.kube.Create(t.Context(), unrelated))
	require.NoError(t, service.ValidateModel(t.Context(), model, nil))
	// A referenced template change reaches the same native compiler as an Agent change.
	template := &v1alpha3.AgentTemplate{}
	require.NoError(t, service.kube.Get(t.Context(), client.ObjectKey{Namespace: "team", Name: "template"}, template))
	template.Spec.ModelConfig.Name = "missing"
	require.ErrorContains(t, service.AgentTemplate(t.Context(), template), "ModelConfig")
}
