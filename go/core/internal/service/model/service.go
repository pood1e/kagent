package model

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	"github.com/kagent-dev/kagent/go/core/internal/service/resourceaccess"
	"github.com/kagent-dev/kagent/go/core/internal/service/secretmaterial"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	common "github.com/kagent-dev/kagent/go/core/internal/utils"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

var modelConfigGVK = v1alpha3.GroupVersion.WithKind("ModelConfig")

// modelConfigResource names ModelConfig in authorization decisions.
const (
	modelConfigResource         = "ModelConfig"
	modelProviderConfigResource = "ModelProviderConfig"
)

type Service struct {
	kubeClient             client.Client
	modelConfigs           *kubecrud.Service[*v1alpha3.ModelConfig, *v1alpha3.ModelConfigList]
	providerConfigs        *kubecrud.Service[*v1alpha3.ModelProviderConfig, *v1alpha3.ModelProviderConfigList]
	defaultNamespace       string
	providerModelRefresher ProviderModelRefresher
	admission              Admission
}

type ListRequest struct{}

type GetRequest struct {
	Ref types.NamespacedName
}

type CreateRequest struct {
	Ref     string
	APIKey  string
	Spec    v1alpha3.ModelConfigSpec
	Secrets []secretmaterial.Material
}

type UpdateRequest struct {
	Ref     types.NamespacedName
	APIKey  *string
	Spec    v1alpha3.ModelConfigSpec
	Secrets []secretmaterial.Material
}

type DeleteRequest struct {
	Ref types.NamespacedName
}

func NewService(kubeClient client.Client, authorizer auth.CollectionAuthorizer, permissions resourceaccess.Checker, defaultNamespace string, options ...ServiceOption) *Service {
	service := &Service{
		kubeClient: kubeClient,
		modelConfigs: kubecrud.NewNativeService(kubeClient, authorizer, kubecrud.NativeAccess{
			Checker: permissions,
			Kind:    apiv1.AuthorizationScopeKind_AUTHORIZATION_SCOPE_KIND_MODEL_CONNECTION,
			Read:    apiv1.Permission_PERMISSION_MODEL_CONNECTION_READ,
			Create:  apiv1.Permission_PERMISSION_MODEL_CONNECTION_MANAGE,
			Update:  apiv1.Permission_PERMISSION_MODEL_CONNECTION_MANAGE,
			Delete:  apiv1.Permission_PERMISSION_MODEL_CONNECTION_MANAGE,
		}, &v1alpha3.ModelConfig{}, &v1alpha3.ModelConfigList{}, modelConfigResource),
		providerConfigs:  kubecrud.NewScopedService(kubeClient, authorizer, &v1alpha3.ModelProviderConfig{}, &v1alpha3.ModelProviderConfigList{}, modelProviderConfigResource),
		defaultNamespace: defaultNamespace,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) List(ctx context.Context, _ ListRequest) (*v1alpha3.ModelConfigList, error) {
	items, err := s.modelConfigs.List(ctx, "")
	if err != nil {
		return nil, err
	}
	result := &v1alpha3.ModelConfigList{Items: make([]v1alpha3.ModelConfig, len(items))}
	for index, item := range items {
		result.Items[index] = *item
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, request GetRequest) (*v1alpha3.ModelConfig, error) {
	return s.modelConfigs.Get(ctx, request.Ref)
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (*v1alpha3.ModelConfig, error) {
	ref, err := common.ParseRefString(request.Ref, s.defaultNamespace)
	if err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid Ref", err)
	}

	if err := validateAPIKeySecretRef(request.Spec.APIKeySecret, request.Spec.APIKeySecretKey, request.Spec.Provider); err != nil {
		return nil, err
	}
	if request.APIKey != "" && request.Spec.APIKeySecret != "" && request.Spec.APIKeySecret != ref.Name {
		return nil, serviceerrors.NewInvalidArgument("Inline API key must use the ModelConfig-owned Secret", nil)
	}
	if err := secretmaterial.ValidateMaterials(request.Secrets); err != nil {
		return nil, err
	}

	spec := request.Spec
	if request.APIKey != "" && spec.APIKeySecret == "" && spec.Provider != v1alpha3.ModelProviderOllama {
		spec.APIKeySecret = ref.Name
		spec.APIKeySecretKey = providerAPIKeySecretKey(spec.Provider)
	}

	modelConfig := &v1alpha3.ModelConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ref.Name,
			Namespace: ref.Namespace,
		},
		Spec: spec,
	}
	if s.admission != nil {
		if _, err := s.modelConfigs.Authorize(ctx, auth.VerbCreate, ref); err != nil {
			return nil, err
		}
		if err := s.validateAdmission(ctx, modelConfig, request.APIKey, request.Secrets); err != nil {
			return nil, err
		}
	}
	modelConfig, err = s.modelConfigs.Create(ctx, modelConfig)
	if err != nil {
		return nil, err
	}

	if err := s.createOwnedSecrets(ctx, modelConfig, request); err != nil {
		if rollbackErr := secretmaterial.RollbackOwnerOnCreateFailure(ctx, s.kubeClient, modelConfig); rollbackErr != nil {
			return nil, serviceerrors.NewInternal(
				serviceerrors.MessageOf(err),
				fmt.Errorf("%w; rollback failed: %v", err, rollbackErr),
			)
		}
		return nil, err
	}

	return modelConfig, nil
}

// createOwnedSecrets writes every Secret a new ModelConfig owns, so one failure rolls back the same way.
func (s *Service) createOwnedSecrets(ctx context.Context, modelConfig *v1alpha3.ModelConfig, request CreateRequest) error {
	spec := modelConfig.Spec
	if request.APIKey != "" && spec.Provider != v1alpha3.ModelProviderOllama {
		if err := secretmaterial.CreateOwnedOpaqueSecret(
			ctx,
			s.kubeClient,
			modelConfig,
			modelConfigGVK,
			modelConfig.Name,
			map[string]string{spec.APIKeySecretKey: request.APIKey},
		); err != nil {
			return serviceerrors.NewInternal("Failed to create API key secret", err)
		}
	}
	return secretmaterial.CreateCompanionSecrets(ctx, s.kubeClient, modelConfig, modelConfigGVK, request.Secrets)
}

func (s *Service) Delete(ctx context.Context, request DeleteRequest) error {
	return s.modelConfigs.Delete(ctx, request.Ref, "")
}

func validateAPIKeySecretRef(apiKeySecret, apiKeySecretKey string, provider v1alpha3.ModelProvider) error {
	if apiKeySecret != "" && apiKeySecretKey == "" &&
		provider != v1alpha3.ModelProviderBedrock &&
		provider != v1alpha3.ModelProviderSAPAICore {
		return serviceerrors.NewInvalidArgument("apiKeySecretKey is required when apiKeySecret is set", nil)
	}
	return nil
}

func providerAPIKeySecretKey(provider v1alpha3.ModelProvider) string {
	return fmt.Sprintf("%s_API_KEY", strings.ToUpper(string(provider)))
}

func referencedSecretNames(spec v1alpha3.ModelConfigSpec) map[string]struct{} {
	references := map[string]struct{}{}
	if spec.APIKeySecret != "" {
		references[spec.APIKeySecret] = struct{}{}
	}
	return references
}
