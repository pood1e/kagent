package authorization

import (
	"encoding/base64"
	"fmt"
	"strings"

	"buf.build/go/protovalidate"
	fga "github.com/openfga/go-sdk"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func principalSubject(id string) (string, error) {
	subject := &apiv1.SubjectReference{Subject: &apiv1.SubjectReference_PrincipalId{PrincipalId: id}}
	return encodeSubject(subject)
}

func encodeSubject(subject *apiv1.SubjectReference) (string, error) {
	if err := protovalidate.Validate(subject); err != nil {
		return "", fmt.Errorf("invalid authorization subject: %w", err)
	}
	switch value := subject.Subject.(type) {
	case *apiv1.SubjectReference_PrincipalId:
		return "principal:" + base64.RawURLEncoding.EncodeToString([]byte(value.PrincipalId)), nil
	case *apiv1.SubjectReference_Group:
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(value.Group)
		if err != nil {
			return "", fmt.Errorf("encode identity group: %w", err)
		}
		result := "group:" + base64.RawURLEncoding.EncodeToString(encoded) + "#member"
		if len(result) > 512 {
			return "", fmt.Errorf("identity group exceeds authorization key limit")
		}
		return result, nil
	default:
		return "", fmt.Errorf("unsupported authorization subject")
	}
}

func decodeSubject(value string) (*apiv1.SubjectReference, error) {
	var result apiv1.SubjectReference
	switch {
	case strings.HasPrefix(value, "principal:"):
		id, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "principal:"))
		if err != nil {
			return nil, fmt.Errorf("decode authorization principal: %w", err)
		}
		result.Subject = &apiv1.SubjectReference_PrincipalId{PrincipalId: string(id)}
	case strings.HasPrefix(value, "group:") && strings.HasSuffix(value, "#member"):
		encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(value, "group:"), "#member"))
		if err != nil {
			return nil, fmt.Errorf("decode authorization group: %w", err)
		}
		group := new(apiv1.IdentityGroup)
		if err := proto.Unmarshal(encoded, group); err != nil {
			return nil, fmt.Errorf("decode identity group: %w", err)
		}
		result.Subject = &apiv1.SubjectReference_Group{Group: group}
	default:
		return nil, fmt.Errorf("unsupported stored authorization subject")
	}
	if err := protovalidate.Validate(&result); err != nil {
		return nil, fmt.Errorf("invalid stored authorization subject: %w", err)
	}
	return &result, nil
}

func groupContext(principal auth.Principal, claim string) ([]fga.TupleKey, error) {
	if claim == "" {
		return nil, nil
	}
	value, present := principal.Claims[claim]
	if !present {
		return nil, nil
	}
	issuerClaim, issued := principal.Claims["iss"]
	if !issued {
		// TokenReview identities have no OIDC issuer; their principal bindings
		// remain usable without importing unqualified Kubernetes groups.
		return nil, nil
	}
	issuer, ok := issuerClaim.(string)
	if !ok || issuer == "" {
		return nil, fmt.Errorf("authenticated group claim requires an issuer")
	}
	var groups []string
	switch values := value.(type) {
	case []string:
		groups = values
	case []any:
		for _, value := range values {
			group, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("authenticated group claim must contain strings")
			}
			groups = append(groups, group)
		}
	default:
		return nil, fmt.Errorf("authenticated group claim must be an array")
	}
	if len(groups) > 90 {
		return nil, fmt.Errorf("authenticated group claim exceeds contextual tuple budget")
	}
	user, err := principalSubject(principal.ID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(groups))
	var result []fga.TupleKey
	for _, id := range groups {
		group, err := encodeSubject(&apiv1.SubjectReference{Subject: &apiv1.SubjectReference_Group{
			Group: &apiv1.IdentityGroup{Issuer: issuer, Id: id},
		}})
		if err != nil {
			return nil, err
		}
		if !seen[group] {
			seen[group] = true
			result = append(result, fga.TupleKey{User: user, Relation: "member", Object: strings.TrimSuffix(group, "#member")})
		}
	}
	return result, nil
}
