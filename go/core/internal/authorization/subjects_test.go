package authorization

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func TestSubjectRoundTripAndIssuerIsolation(t *testing.T) {
	for _, subject := range []*apiv1.SubjectReference{
		{Subject: &apiv1.SubjectReference_PrincipalId{PrincipalId: auth.PrincipalID(testIssuer, "alice")}},
		{Subject: &apiv1.SubjectReference_PrincipalId{PrincipalId: auth.PrincipalID("kubernetes", "machine")}},
		{Subject: &apiv1.SubjectReference_Group{Group: &apiv1.IdentityGroup{Issuer: testIssuer, Id: "运维组"}}},
	} {
		encoded, err := encodeSubject(subject)
		require.NoError(t, err)
		decoded, err := decodeSubject(encoded)
		require.NoError(t, err)
		require.Equal(t, subject.String(), decoded.String())
	}
	left, err := encodeSubject(&apiv1.SubjectReference{Subject: &apiv1.SubjectReference_Group{
		Group: &apiv1.IdentityGroup{Issuer: testIssuer, Id: "ops"},
	}})
	require.NoError(t, err)
	right, err := encodeSubject(&apiv1.SubjectReference{Subject: &apiv1.SubjectReference_Group{
		Group: &apiv1.IdentityGroup{Issuer: "https://other.example", Id: "ops"},
	}})
	require.NoError(t, err)
	require.NotEqual(t, left, right)
	for _, value := range []string{"principal:!", "group:!#member", "session_share:token", "group:Zm9v#member"} {
		_, err := decodeSubject(value)
		require.Error(t, err)
	}
}

func TestGroupFactsUseVerifiedPrincipalAndIssuer(t *testing.T) {
	principal := auth.Principal{ID: "alice", Claims: map[string]any{
		"iss": testIssuer, "groups": []any{"ops", "ops", "audit"},
	}}
	facts, err := groupContext(principal, "groups")
	require.NoError(t, err)
	require.Len(t, facts, 2)
	user, err := principalSubject("alice")
	require.NoError(t, err)
	require.Equal(t, user, facts[0].User)
	for _, claim := range []any{"ops", []any{"ops", 123}, make([]string, 91)} {
		principal.Claims["groups"] = claim
		_, err := groupContext(principal, "groups")
		require.Error(t, err)
	}
	delete(principal.Claims, "iss")
	principal.Claims["groups"] = []string{"ops"}
	facts, err = groupContext(principal, "groups")
	require.NoError(t, err)
	require.Empty(t, facts)
	principal.Claims["iss"] = 123
	_, err = groupContext(principal, "groups")
	require.Error(t, err)
}
