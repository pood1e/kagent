package auth

import (
	"fmt"
	"maps"
	"slices"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// MaxPermissionChecks is the largest CheckPermissionsRequest the API accepts.
const MaxPermissionChecks = 50

// CanonicalResourceGrants validates references and merges exact resources
// into deterministic action sets. An absent set remains absent.
func CanonicalResourceGrants(input *apiv1.ResourceGrantSet) (*apiv1.ResourceGrantSet, error) {
	if input == nil {
		return nil, nil
	}
	if err := protovalidate.Validate(input); err != nil {
		return nil, err
	}
	byResource := make(map[string]*apiv1.ResourceGrant)
	for _, grant := range input.Grants {
		key, err := proto.MarshalOptions{Deterministic: true}.Marshal(grant.Resource)
		if err != nil {
			return nil, err
		}
		current := byResource[string(key)]
		if current == nil {
			current = &apiv1.ResourceGrant{Resource: proto.CloneOf(grant.Resource)}
			byResource[string(key)] = current
		}
		current.Permissions = append(current.Permissions, grant.Permissions...)
	}
	result := &apiv1.ResourceGrantSet{}
	for _, key := range slices.Sorted(maps.Keys(byResource)) {
		grant := byResource[key]
		slices.Sort(grant.Permissions)
		grant.Permissions = slices.Compact(grant.Permissions)
		result.Grants = append(result.Grants, grant)
	}
	return result, nil
}

// ValidateResourceCeiling requires immutable Kubernetes identities after name
// resolution. Admission candidates may be canonical before Kubernetes assigns a UID.
func ValidateResourceCeiling(input *apiv1.ResourceGrantSet) error {
	if input == nil {
		return nil
	}
	if err := protovalidate.Validate(input); err != nil {
		return err
	}
	for _, grant := range input.GetGrants() {
		switch ref := grant.Resource.Resource.(type) {
		case *apiv1.AuthorizationResource_Kubernetes:
			if ref.Kubernetes.Uid == "" {
				return fmt.Errorf("resource ceiling requires a Kubernetes UID")
			}
		case *apiv1.AuthorizationResource_McpTool:
			if ref.McpTool.Server.Uid == "" {
				return fmt.Errorf("tool ceiling requires a server UID")
			}
		case *apiv1.AuthorizationResource_Workspace:
			if ref.Workspace.Uid == "" {
				return fmt.Errorf("resource ceiling requires a workspace UID")
			}
		}
	}
	return nil
}

// ResourceGrantAllows requires an exact canonical resource and action; server
// use and workspace roles never act as wildcard grants in an execution ceiling.
func ResourceGrantAllows(set *apiv1.ResourceGrantSet, resource *apiv1.AuthorizationResource, permission apiv1.Permission) bool {
	for _, grant := range set.GetGrants() {
		if proto.Equal(grant.Resource, resource) && slices.Contains(grant.Permissions, permission) {
			return true
		}
	}
	return false
}

// IntersectResourceGrants returns an independently owned, narrowed set.
func IntersectResourceGrants(grants, ceiling *apiv1.ResourceGrantSet) *apiv1.ResourceGrantSet {
	result := &apiv1.ResourceGrantSet{}
	for _, grant := range grants.GetGrants() {
		narrowed := &apiv1.ResourceGrant{Resource: proto.CloneOf(grant.Resource)}
		for _, permission := range grant.Permissions {
			if ResourceGrantAllows(ceiling, grant.Resource, permission) {
				narrowed.Permissions = append(narrowed.Permissions, permission)
			}
		}
		if len(narrowed.Permissions) > 0 {
			result.Grants = append(result.Grants, narrowed)
		}
	}
	return result
}

// ResourceGrantsWithin reports whether every exact action is bounded by ceiling.
func ResourceGrantsWithin(grants, ceiling *apiv1.ResourceGrantSet) bool {
	for _, grant := range grants.GetGrants() {
		for _, permission := range grant.Permissions {
			if !ResourceGrantAllows(ceiling, grant.Resource, permission) {
				return false
			}
		}
	}
	return true
}
