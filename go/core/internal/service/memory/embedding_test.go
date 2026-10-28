package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

func TestModelEmbedderUsesNativeModelAndLiveSecret(t *testing.T) {
	key := "first"
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/embeddings", r.URL.Path)
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		require.Equal(t, "native", r.Header.Get("X-Embedding"))
		var input struct {
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
			Input      []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "embedding-model", input.Model)
		require.Equal(t, VectorDimension, input.Dimensions)
		require.Equal(t, []string{"second", "first"}, input.Input)
		// The SDK response index, rather than transport order, determines input pairing.
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 1, "embedding": vector(VectorDimension, .25)},
			map[string]any{"index": 0, "embedding": vector(VectorDimension, .5)},
		}}))
	}))
	defer provider.Close()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	model := &v1alpha3.ModelConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "embedding"}, Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "embedding-model", APIKeySecret: "key", APIKeySecretKey: "token", OpenAI: &v1alpha3.OpenAIConfig{BaseURL: provider.URL}, DefaultHeaders: map[string]string{"X-Embedding": "native"}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "key"}, Data: map[string][]byte{"token": []byte(key)}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model, secret).Build()
	embedder := &ModelEmbedder{Kube: kube, Model: &apiv1.ResourceReference{Namespace: "models", Name: "embedding"}}
	for _, next := range []string{"first", "rotated"} {
		key = next
		secret.Data["token"] = []byte(key)
		require.NoError(t, kube.Update(t.Context(), secret))
		vectors, err := embedder.Generate(t.Context(), []string{"second", "first"})
		require.NoError(t, err)
		require.Equal(t, float32(.5), vectors[0][0])
		require.Equal(t, float32(.25), vectors[1][0])
	}
	require.Equal(t, 2, calls)
	model.Spec.Provider = v1alpha3.ModelProviderAnthropic
	require.NoError(t, kube.Update(t.Context(), model))
	_, err := embedder.Generate(t.Context(), []string{"denied"})
	require.Error(t, err)
	require.Equal(t, 2, calls)
}

func TestModelEmbedderReadsRotatingProjectedCredential(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	var received []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector(VectorDimension, .5)}}}))
	}))
	defer provider.Close()
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	model := &v1alpha3.ModelConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "embedding"}, Spec: v1alpha3.ModelConfigSpec{Provider: v1alpha3.ModelProviderOpenAI, Model: "embedding", TokenPassthrough: new(true), OpenAI: &v1alpha3.OpenAIConfig{BaseURL: provider.URL}}}
	embedder := &ModelEmbedder{Kube: fake.NewClientBuilder().WithScheme(scheme).WithObjects(model).Build(), Model: &apiv1.ResourceReference{Namespace: "models", Name: "embedding"}, BearerTokenFile: file}
	for _, token := range []string{"first", "rotated"} {
		require.NoError(t, os.WriteFile(file, []byte(token), 0o600))
		_, err := embedder.Generate(t.Context(), []string{"text"})
		require.NoError(t, err)
	}
	require.Equal(t, []string{"Bearer first", "Bearer rotated"}, received)
	require.NoError(t, os.Remove(file))
	_, err := embedder.Generate(t.Context(), []string{"text"})
	require.Error(t, err)
	require.Len(t, received, 2, "missing service credentials fail closed")
}
