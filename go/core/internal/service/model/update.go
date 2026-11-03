package model

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/secretmaterial"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// Update keeps its own write because the owned Secrets must land between the
// authorized read and the retrying ModelConfig write; the update is authorized first.
func (s *Service) Update(ctx context.Context, request UpdateRequest) (*v1alpha3.ModelConfig, error) {
	authorizedUID, err := s.modelConfigs.Authorize(ctx, auth.VerbUpdate, request.Ref)
	if err != nil {
		return nil, err
	}
	modelConfig, err := s.modelConfigs.Get(ctx, request.Ref)
	if err != nil {
		return nil, err
	}
	if modelConfig.UID != authorizedUID {
		return nil, serviceerrors.NewNotFound("ModelConfig identity changed", nil)
	}
	if err := validateAPIKeySecretRef(request.Spec.APIKeySecret, request.Spec.APIKeySecretKey, request.Spec.Provider); err != nil {
		return nil, err
	}
	if request.APIKey != nil && *request.APIKey != "" && request.Spec.APIKeySecret != "" && request.Spec.APIKeySecret != request.Ref.Name {
		return nil, serviceerrors.NewInvalidArgument("Inline API key must use the ModelConfig-owned Secret", nil)
	}
	if err := secretmaterial.ValidateMaterials(request.Secrets); err != nil {
		return nil, err
	}

	oldRefs := referencedSecretNames(modelConfig.Spec)
	spec := request.Spec
	if request.APIKey != nil && *request.APIKey != "" && spec.APIKeySecret == "" && spec.Provider != v1alpha3.ModelProviderOllama {
		spec.APIKeySecret = request.Ref.Name
		spec.APIKeySecretKey = providerAPIKeySecretKey(spec.Provider)
	}

	candidate := modelConfig.DeepCopy()
	candidate.Spec = spec
	var pendingKey string
	if request.APIKey != nil {
		pendingKey = *request.APIKey
	}
	if err := s.validateAdmission(ctx, candidate, pendingKey, request.Secrets); err != nil {
		return nil, err
	}

	if request.APIKey != nil && *request.APIKey != "" && spec.Provider != v1alpha3.ModelProviderOllama {
		if err := secretmaterial.CreateOrUpdateOwnedOpaqueSecret(
			ctx,
			s.kubeClient,
			modelConfig,
			modelConfigGVK,
			modelConfig.Name,
			map[string]string{spec.APIKeySecretKey: *request.APIKey},
		); err != nil {
			if serviceerrors.CodeOf(err) != "" {
				return nil, err
			}
			return nil, serviceerrors.NewInternal("Failed to update API key secret", err)
		}
	}

	if err := secretmaterial.CreateCompanionSecrets(ctx, s.kubeClient, modelConfig, modelConfigGVK, request.Secrets); err != nil {
		return nil, err
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha3.ModelConfig{}
		if err := s.kubeClient.Get(ctx, request.Ref, latest); err != nil {
			return err
		}
		if latest.UID != authorizedUID {
			return serviceerrors.NewNotFound("ModelConfig identity changed", nil)
		}
		latest.Spec = spec
		if err := s.kubeClient.Update(ctx, latest); err != nil {
			return err
		}
		modelConfig = latest
		return nil
	}); err != nil {
		if serviceerrors.CodeOf(err) != "" {
			return nil, err
		}
		if apierrors.IsInvalid(err) {
			return nil, serviceerrors.NewInvalidArgument(err.Error(), err)
		}
		return nil, serviceerrors.NewInternal("Failed to update ModelConfig", err)
	}

	newRefs := referencedSecretNames(modelConfig.Spec)
	requestedNames := map[string]struct{}{}
	for _, material := range request.Secrets {
		requestedNames[material.Name] = struct{}{}
	}
	for name := range oldRefs {
		if _, kept := newRefs[name]; kept {
			continue
		}
		if _, kept := requestedNames[name]; kept {
			continue
		}
		if err := secretmaterial.DeleteOwnedSecret(ctx, s.kubeClient, modelConfig, modelConfigGVK, name); err != nil {
			return nil, serviceerrors.NewInternal("Failed to update ModelConfig", err)
		}
	}

	return modelConfig, nil
}
