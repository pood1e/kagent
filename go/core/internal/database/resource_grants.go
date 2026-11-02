package database

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// encodeResourceGrants stores one canonical protobuf set. SQL NULL preserves an
// absent ceiling; an empty bytea is an explicitly empty, bounded set.
func encodeResourceGrants(grants *apiv1.ResourceGrantSet) ([]byte, error) {
	canonical, err := auth.CanonicalResourceGrants(grants)
	if err != nil || canonical == nil {
		return nil, err
	}
	if err := auth.ValidateResourceCeiling(canonical); err != nil {
		return nil, err
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(canonical)
	if data == nil && err == nil {
		data = []byte{}
	}
	return data, err
}

// decodeResourceGrants validates durable identities and preserves absence.
func decodeResourceGrants(data []byte) (*apiv1.ResourceGrantSet, error) {
	if data == nil {
		return nil, nil
	}
	grants := &apiv1.ResourceGrantSet{}
	if err := proto.Unmarshal(data, grants); err != nil {
		return nil, fmt.Errorf("decode resource grants: %w", err)
	}
	canonical, err := auth.CanonicalResourceGrants(grants)
	if err != nil {
		return nil, err
	}
	if err := auth.ValidateResourceCeiling(canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

// canonicalDependencyManifest keeps direct and delegated capabilities separate
// while validating both sets as stable, UID-bound revision inputs.
func canonicalDependencyManifest(input *apiv1.ExecutionDependencyManifest) (*apiv1.ExecutionDependencyManifest, error) {
	if input == nil {
		return nil, nil
	}
	if input.Direct == nil {
		return nil, fmt.Errorf("dependency manifest requires direct capabilities")
	}
	direct, err := auth.CanonicalResourceGrants(input.Direct)
	if err != nil {
		return nil, err
	}
	delegated, err := auth.CanonicalResourceGrants(input.Delegated)
	if err != nil {
		return nil, err
	}
	if err := auth.ValidateResourceCeiling(direct); err != nil {
		return nil, err
	}
	if err := auth.ValidateResourceCeiling(delegated); err != nil {
		return nil, err
	}
	return &apiv1.ExecutionDependencyManifest{Direct: direct, Delegated: delegated}, nil
}

// encodeDependencyManifest stores one immutable, canonical Proto manifest.
func encodeDependencyManifest(input *apiv1.ExecutionDependencyManifest) ([]byte, error) {
	manifest, err := canonicalDependencyManifest(input)
	if err != nil || manifest == nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
}

// decodeDependencyManifest rejects malformed durable input and preserves absence.
func decodeDependencyManifest(data []byte) (*apiv1.ExecutionDependencyManifest, error) {
	if data == nil {
		return nil, nil
	}
	manifest := &apiv1.ExecutionDependencyManifest{}
	if err := proto.Unmarshal(data, manifest); err != nil {
		return nil, fmt.Errorf("decode dependency manifest: %w", err)
	}
	return canonicalDependencyManifest(manifest)
}
