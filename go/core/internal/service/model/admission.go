package model

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/secretmaterial"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
)

// Admission checks configuration and pending Secrets without applying changes.
// The model service retains authorization and Secret ownership checks.
type Admission interface {
	ValidateModel(context.Context, *v1alpha3.ModelConfig, []*corev1.Secret) error
}

func WithAdmission(admission Admission) ServiceOption {
	return func(service *Service) { service.admission = admission }
}

func (s *Service) validateAdmission(ctx context.Context, model *v1alpha3.ModelConfig, apiKey string, materials []secretmaterial.Material) error {
	if s.admission == nil {
		return nil
	}
	pending := append([]secretmaterial.Material(nil), materials...)
	if apiKey != "" && model.Spec.Provider != v1alpha3.ModelProviderOllama {
		pending = append(pending, secretmaterial.Material{Name: model.Name, Key: model.Spec.APIKeySecretKey, Value: apiKey})
	}
	byName := make(map[string]*corev1.Secret)
	for _, material := range pending {
		secret := byName[material.Name]
		if secret == nil {
			secret = &corev1.Secret{}
			if err := s.kubeClient.Get(ctx, client.ObjectKey{Namespace: model.Namespace, Name: material.Name}, secret); err != nil {
				if !apierrors.IsNotFound(err) {
					return serviceerrors.NewInternal("Failed to read pending model Secret", err)
				}
				secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: model.Namespace, Name: material.Name}}
			}
			byName[material.Name] = secret
		}
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}
		secret.Data[material.Key] = []byte(material.Value)
	}
	secrets := make([]*corev1.Secret, 0, len(byName))
	for _, secret := range byName {
		secrets = append(secrets, secret)
	}
	return s.admission.ValidateModel(ctx, model, secrets)
}
