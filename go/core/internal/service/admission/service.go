// Package admission validates candidate configuration through the native
// Harness compilers before the control plane persists a change.
package admission

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	_, err := s.validate(ctx, agent, nil, nil, nil)
	return err
}
func (s *Service) AgentTemplate(ctx context.Context, template *v1alpha3.AgentTemplate) error {
	_, err := s.validate(ctx, template, nil, nil, nil)
	return err
}
func (s *Service) Harness(ctx context.Context, harness *v1alpha3.Harness) error {
	_, err := s.validate(ctx, harness, nil, nil, nil)
	return err
}

// Workflow validates a candidate document using the native compilation inputs,
// without publishing its ConfigMap or preparing a runtime revision.
func (s *Service) Workflow(ctx context.Context, agent *v1alpha3.Agent, template *v1alpha3.AgentTemplate, document []byte) (string, error) {
	return s.validate(ctx, agent, nil, document, template)
}

// WorkflowRevision compiles persisted inputs without preparing or changing them.
// The caller authorizes access to the Agent before resolving its dependencies.
func (s *Service) WorkflowRevision(ctx context.Context, ref types.NamespacedName) (string, error) {
	agent := &v1alpha3.Agent{}
	if err := s.kube.Get(ctx, ref, agent); err != nil {
		if apierrors.IsNotFound(err) {
			return "", serviceerrors.NewNotFound("Agent not found", err)
		}
		return "", serviceerrors.NewInternal("Failed to read Agent", err)
	}
	return s.validate(ctx, agent, nil, nil, nil)
}

// ValidateModel includes pending credential writes in the read-only snapshot.
// The caller owns authorization and persistence of those Secrets.
func (s *Service) ValidateModel(ctx context.Context, model *v1alpha3.ModelConfig, secrets []*corev1.Secret) error {
	_, err := s.validate(ctx, model, secrets, nil, nil)
	return err
}

func (s *Service) validate(ctx context.Context, incoming client.Object, secrets []*corev1.Secret, document []byte, template *v1alpha3.AgentTemplate) (string, error) {
	snapshot, err := loadSnapshot(ctx, s.kube, incoming.GetNamespace())
	if err != nil {
		return "", serviceerrors.NewInternal("Failed to load compilation inputs", err)
	}
	for _, secret := range secrets {
		snapshot.secrets = replace(snapshot.secrets, secret)
	}
	if len(document) > 0 {
		agent, ok := incoming.(*v1alpha3.Agent)
		if !ok {
			return "", serviceerrors.NewInvalidArgument("Candidate requires an Agent", nil)
		}
		var spec *v1alpha3.AgentTemplateSpec
		if template != nil {
			if template.Namespace != agent.Namespace || agent.Spec.Template.Ref == nil || template.Name != agent.Spec.Template.Ref.Name {
				return "", serviceerrors.NewInvalidArgument("Candidate template reference mismatch", nil)
			}
			snapshot.templates = replace(snapshot.templates, template)
			spec = &template.Spec
		} else {
			spec = agent.Spec.Template.Inline
		}
		if spec == nil || spec.WorkflowFrom == nil {
			return "", serviceerrors.NewInvalidArgument("Candidate requires a workflow template", nil)
		}
		ref := spec.WorkflowFrom
		candidate := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: agent.Namespace, Name: ref.Name}, Data: map[string]string{}}
		for _, current := range snapshot.configMaps {
			if current.Name == ref.Name {
				candidate = current.DeepCopy()
				break
			}
		}
		if candidate.Data == nil {
			candidate.Data = map[string]string{}
		}
		candidate.Data[ref.Key] = string(document)
		snapshot.configMaps = replace(snapshot.configMaps, candidate)
	}
	switch object := incoming.(type) {
	case *v1alpha3.Agent:
		snapshot.agents = replace(snapshot.agents, object)
	case *v1alpha3.AgentTemplate:
		snapshot.templates = replace(snapshot.templates, object)
	case *v1alpha3.Harness:
		snapshot.harnesses = replace(snapshot.harnesses, object)
	case *v1alpha3.ModelConfig:
		snapshot.models = replace(snapshot.models, object)
	default:
		return "", serviceerrors.NewInvalidArgument("Unsupported compilation input", nil)
	}
	collections, err := snapshot.collections()
	if err != nil {
		return "", serviceerrors.NewInvalidArgument("Invalid compilation inputs", err)
	}
	compiler := compilation.New(nil, collections, s.settings, s.images)
	var revision string
	for _, agent := range snapshot.agents {
		if !snapshot.affected(agent, incoming) {
			continue
		}
		compiled, err := compiler.CompileAgent(ctx, agent)
		if err != nil {
			return "", serviceerrors.NewInvalidArgument(fmt.Sprintf("Agent %s: %s", client.ObjectKeyFromObject(agent), err), err)
		}
		digest, err := compiled.Digest()
		if err != nil {
			return "", serviceerrors.NewInternal("Failed to derive workflow revision", err)
		}
		revision = digest.String()
	}
	return revision, nil
}

func (s *snapshot) affected(agent *v1alpha3.Agent, incoming client.Object) bool {
	switch object := incoming.(type) {
	case *v1alpha3.Agent:
		return client.ObjectKeyFromObject(agent) == client.ObjectKeyFromObject(object)
	case *v1alpha3.Harness:
		return agent.Spec.HarnessRef.Name == object.Name
	}
	if model, ok := incoming.(*v1alpha3.ModelConfig); ok && agent.Spec.ModelConfigRef != nil && agent.Spec.ModelConfigRef.Name == model.Name {
		return true
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
