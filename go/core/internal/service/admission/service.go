// Package admission validates candidate configuration through the native
// Harness compilers before the control plane persists a change.
package admission

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/internal/translator/compilation"
)

type Service struct {
	kube     client.Client
	settings *configv1.RuntimeDefaultsConfig
	images   *configv1.HarnessImagesConfig
}

func NewService(kube client.Client, settings *configv1.RuntimeDefaultsConfig, images *configv1.HarnessImagesConfig) *Service {
	return &Service{kube: kube, settings: settings, images: images}
}

// Agent validates an inline or referenced Agent without applying its revision.
func (s *Service) Agent(ctx context.Context, agent *v1alpha3.Agent) error {
	return s.validate(ctx, agent, nil)
}
func (s *Service) AgentTemplate(ctx context.Context, template *v1alpha3.AgentTemplate) error {
	return s.validate(ctx, template, nil)
}
func (s *Service) Harness(ctx context.Context, harness *v1alpha3.Harness) error {
	return s.validate(ctx, harness, nil)
}

// ValidateModel includes pending credential writes in the read-only snapshot.
// The caller owns authorization and persistence of those Secrets.
func (s *Service) ValidateModel(ctx context.Context, model *v1alpha3.ModelConfig, secrets []*corev1.Secret) error {
	return s.validate(ctx, model, secrets)
}

func (s *Service) validate(ctx context.Context, incoming client.Object, secrets []*corev1.Secret) error {
	snapshot, err := loadSnapshot(ctx, s.kube, incoming.GetNamespace())
	if err != nil {
		return serviceerrors.NewInternal("Failed to load compilation inputs", err)
	}
	for _, secret := range secrets {
		snapshot.secrets = replace(snapshot.secrets, secret)
	}
	switch object := incoming.(type) {
	case *v1alpha3.Agent:
		snapshot.agents = []*v1alpha3.Agent{object}
	case *v1alpha3.AgentTemplate:
		snapshot.templates = replace(snapshot.templates, object)
	case *v1alpha3.Harness:
		snapshot.harnesses = replace(snapshot.harnesses, object)
	case *v1alpha3.ModelConfig:
		snapshot.models = replace(snapshot.models, object)
	default:
		return serviceerrors.NewInvalidArgument("Unsupported compilation input", nil)
	}
	collections, err := snapshot.collections()
	if err != nil {
		return serviceerrors.NewInvalidArgument("Invalid compilation inputs", err)
	}
	compiler := compilation.New(nil, collections, s.settings, s.images)
	for _, agent := range snapshot.agents {
		if !snapshot.affected(agent, incoming) {
			continue
		}
		if _, err := compiler.CompileAgent(ctx, agent); err != nil {
			return serviceerrors.NewInvalidArgument(fmt.Sprintf("Agent %s: %s", client.ObjectKeyFromObject(agent), err), err)
		}
	}
	return nil
}

func (s *snapshot) affected(agent *v1alpha3.Agent, incoming client.Object) bool {
	switch object := incoming.(type) {
	case *v1alpha3.Agent:
		return true
	case *v1alpha3.Harness:
		return agent.Spec.HarnessRef.Name == object.Name
	}
	seen := make(map[string]bool)
	var visit func(*v1alpha3.AgentTemplateSpec) bool
	reference := func(name string) bool {
		if object, ok := incoming.(*v1alpha3.AgentTemplate); ok && name == object.Name {
			return true
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		for _, template := range s.templates {
			if template.Name == name {
				return visit(&template.Spec)
			}
		}
		return false
	}
	visit = func(spec *v1alpha3.AgentTemplateSpec) bool {
		if spec == nil {
			return false
		}
		if model, ok := incoming.(*v1alpha3.ModelConfig); ok && spec.ModelConfig != nil && spec.ModelConfig.Name == model.Name {
			return true
		}
		for _, tool := range spec.Tools {
			if tool.SubAgent != nil && tool.SubAgent.TemplateRef != nil && reference(tool.SubAgent.TemplateRef.Name) {
				return true
			}
		}
		return false
	}
	if agent.Spec.Template.Ref != nil {
		return reference(agent.Spec.Template.Ref.Name)
	}
	return visit(agent.Spec.Template.Inline)
}
