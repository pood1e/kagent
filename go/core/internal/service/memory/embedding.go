package memory

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/transport"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

// ModelEmbedder resolves live model credentials for control-plane text requests.
// The configured reference is administrator-owned, independent of caller inputs.
type ModelEmbedder struct {
	Kube            client.Reader
	Model           *apiv1.ResourceReference
	BearerTokenFile string
}

func (e *ModelEmbedder) Generate(ctx context.Context, texts []string) ([][]float32, error) {
	if e.Model.GetNamespace() == "" || e.Model.GetName() == "" {
		return nil, fmt.Errorf("embedding ModelConfig is required")
	}
	model := &v1alpha3.ModelConfig{}
	if err := e.Kube.Get(ctx, types.NamespacedName{Namespace: e.Model.Namespace, Name: e.Model.Name}, model); err != nil {
		return nil, fmt.Errorf("resolve embedding ModelConfig: %w", err)
	}
	spec := model.Spec
	if spec.Provider != v1alpha3.ModelProviderOpenAI || spec.ActorJWT != nil || !spec.TLS.IsEmpty() {
		return nil, fmt.Errorf("control-plane embeddings require an OpenAI-compatible ModelConfig with server credentials")
	}
	// An explicit empty key prevents ambient controller environment credentials.
	key := ""
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if ptr.Deref(spec.TokenPassthrough, false) {
		if e.BearerTokenFile == "" {
			return nil, fmt.Errorf("embedding service credential file is required")
		}
		roundTripper, err := transport.NewBearerAuthWithRefreshRoundTripper("", e.BearerTokenFile, http.DefaultTransport)
		if err != nil {
			return nil, fmt.Errorf("embedding service credential unavailable")
		}
		httpClient.Transport = roundTripper
	}
	if !ptr.Deref(spec.TokenPassthrough, false) && spec.APIKeySecret != "" {
		secret := &corev1.Secret{}
		if err := e.Kube.Get(ctx, types.NamespacedName{Namespace: model.Namespace, Name: spec.APIKeySecret}, secret); err != nil {
			return nil, fmt.Errorf("resolve embedding credentials: %w", err)
		}
		value, ok := secret.Data[spec.APIKeySecretKey]
		if !ok || len(value) == 0 {
			return nil, fmt.Errorf("embedding credential key is missing")
		}
		key = string(value)
	}
	options := []option.RequestOption{option.WithAPIKey(key), option.WithBaseURL("https://api.openai.com/v1"), option.WithHTTPClient(httpClient)}
	if spec.OpenAI != nil && spec.OpenAI.BaseURL != "" {
		options = append(options, option.WithBaseURL(spec.OpenAI.BaseURL))
	}
	for name, value := range spec.DefaultHeaders {
		if strings.EqualFold(name, "Authorization") {
			return nil, fmt.Errorf("embedding defaultHeaders must preserve credential ownership")
		}
		options = append(options, option.WithHeader(name, value))
	}
	sdk := openai.NewClient(options...)
	response, err := sdk.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(spec.Model), Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts}, Dimensions: openai.Int(VectorDimension),
	})
	if err != nil {
		return nil, fmt.Errorf("embedding provider request failed")
	}
	if len(response.Data) != len(texts) {
		return nil, fmt.Errorf("embedding response count mismatch")
	}
	vectors := make([][]float32, len(texts))
	for _, item := range response.Data {
		index := int(item.Index)
		if index < 0 || index >= len(vectors) || vectors[index] != nil || len(item.Embedding) != VectorDimension {
			return nil, fmt.Errorf("invalid embedding response")
		}
		vector := make([]float32, len(item.Embedding))
		for i, value := range item.Embedding {
			vector[i] = float32(value)
		}
		if !finiteVector(vector) {
			return nil, fmt.Errorf("invalid embedding response")
		}
		vectors[index] = vector
	}
	return vectors, nil
}
