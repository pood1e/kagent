package memory

import (
	"context"
	"math"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

// Embedder generates vectors at the control-plane integration boundary.
type Embedder interface {
	Generate(context.Context, []string) ([][]float32, error)
}

func WithEmbedder(embedder Embedder) Option { return func(s *Service) { s.embedder = embedder } }

func (s *Service) vector(ctx context.Context, supplied []float32, text string) ([]float32, error) {
	if len(supplied) != 0 {
		return supplied, validateVector(supplied)
	}
	if strings.TrimSpace(text) == "" || len(text) > 32768 {
		return nil, serviceerrors.NewInvalidArgument("Embedding text must contain 1 to 32768 bytes", nil)
	}
	if s.embedder == nil {
		return nil, serviceerrors.NewFailedPrecondition("Control-plane embedding model is not configured", nil)
	}
	vectors, err := s.embedder.Generate(ctx, []string{text})
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Memory embedding unavailable", err)
	}
	if len(vectors) != 1 {
		return nil, serviceerrors.NewInternal("Embedding response count mismatch", nil)
	}
	if err := validateVector(vectors[0]); err != nil {
		return nil, serviceerrors.NewInternal("Invalid embedding response", err)
	}
	return vectors[0], nil
}

func finiteVector(vector []float32) bool {
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

// batchVectors validates the complete batch before a single provider request.
func (s *Service) batchVectors(ctx context.Context, inputs []Input) ([]Input, error) {
	result := slices.Clone(inputs)
	var indexes []int
	var texts []string
	for i, input := range result {
		if !proto.Equal(input.Scope, result[0].Scope) {
			return nil, serviceerrors.NewInvalidArgument("batch requires one memory scope", nil)
		}
		if len(input.Vector) > 0 {
			if err := validateVector(input.Vector); err != nil {
				return nil, err
			}
		} else {
			if strings.TrimSpace(input.Content) == "" || len(input.Content) > 32768 {
				return nil, serviceerrors.NewInvalidArgument("Embedding text must contain 1 to 32768 bytes", nil)
			}
			indexes = append(indexes, i)
			texts = append(texts, input.Content)
		}
	}
	if len(indexes) == 0 {
		return result, nil
	}
	if s.embedder == nil {
		return nil, serviceerrors.NewFailedPrecondition("Control-plane embedding model is not configured", nil)
	}
	vectors, err := s.embedder.Generate(ctx, texts)
	if err != nil {
		return nil, serviceerrors.NewUnavailable("Memory embedding unavailable", err)
	}
	if len(vectors) != len(indexes) {
		return nil, serviceerrors.NewInternal("Embedding response count mismatch", nil)
	}
	for position, index := range indexes {
		if err := validateVector(vectors[position]); err != nil {
			return nil, serviceerrors.NewInternal("Invalid embedding response", err)
		}
		result[index].Vector = vectors[position]
	}
	return result, nil
}
