package admission

import (
	"context"

	atev1alpha1 "go.ate.dev/substrate/pkg/api/v1alpha1"
	"istio.io/istio/pkg/kube/krt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/translator"
)

type snapshot struct {
	agents     []*v1alpha3.Agent
	templates  []*v1alpha3.AgentTemplate
	harnesses  []*v1alpha3.Harness
	models     []*v1alpha3.ModelConfig
	servers    []*v1alpha3.RemoteMCPServer
	configMaps []*corev1.ConfigMap
	secrets    []*corev1.Secret
	pools      []*atev1alpha1.WorkerPool
}

func loadSnapshot(ctx context.Context, kube client.Client, namespace string) (*snapshot, error) {
	s := &snapshot{}
	for _, read := range []func() error{
		func() error { return readList(ctx, kube, namespace, &v1alpha3.AgentList{}, &s.agents) },
		func() error { return readList(ctx, kube, namespace, &v1alpha3.AgentTemplateList{}, &s.templates) },
		func() error { return readList(ctx, kube, namespace, &v1alpha3.HarnessList{}, &s.harnesses) },
		func() error { return readList(ctx, kube, namespace, &v1alpha3.ModelConfigList{}, &s.models) },
		func() error { return readList(ctx, kube, namespace, &v1alpha3.RemoteMCPServerList{}, &s.servers) },
		func() error { return readList(ctx, kube, namespace, &corev1.ConfigMapList{}, &s.configMaps) },
		func() error { return readList(ctx, kube, namespace, &corev1.SecretList{}, &s.secrets) },
		func() error { return readList(ctx, kube, namespace, &atev1alpha1.WorkerPoolList{}, &s.pools) },
	} {
		if err := read(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func readList[T client.Object](ctx context.Context, kube client.Client, namespace string, list client.ObjectList, target *[]T) error {
	if err := kube.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return err
	}
	return meta.EachListItem(list, func(item runtime.Object) error {
		*target = append(*target, item.(T))
		return nil
	})
}

func replace[T client.Object](items []T, incoming T) []T {
	for i, item := range items {
		if client.ObjectKeyFromObject(item) == client.ObjectKeyFromObject(incoming) {
			items[i] = incoming
			return items
		}
	}
	return append(items, incoming)
}

func (s *snapshot) collections() (translator.Collections, error) {
	collections := translator.Collections{
		AgentTemplates:   krt.NewStaticCollection(nil, s.templates),
		Harnesses:        krt.NewStaticCollection(nil, s.harnesses),
		RemoteMCPServers: krt.NewStaticCollection(nil, s.servers),
		ConfigMaps:       krt.NewStaticCollection(nil, s.configMaps),
		Secrets:          krt.NewStaticCollection(nil, s.secrets),
		WorkerPools:      krt.NewStaticCollection(nil, s.pools),
	}
	models := make([]translator.ResolvedModelConfig, 0, len(s.models))
	for _, model := range s.models {
		resolved, err := translator.ResolveModelConfig(nil, collections, model)
		if err != nil {
			return translator.Collections{}, err
		}
		models = append(models, *resolved)
	}
	collections.ResolvedModelConfigs = krt.NewStaticCollection(nil, models)
	return collections, nil
}
