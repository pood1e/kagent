package grpcserver

import (
	"maps"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// applyResourceMetadata preserves server-owned metadata and the caller's write precondition.
func applyResourceMetadata(existing, incoming metav1.Object) {
	existing.SetLabels(maps.Clone(incoming.GetLabels()))
	existing.SetAnnotations(maps.Clone(incoming.GetAnnotations()))
	existing.SetOwnerReferences(slices.Clone(incoming.GetOwnerReferences()))
	if version := incoming.GetResourceVersion(); version != "" {
		existing.SetResourceVersion(version)
	}
}
