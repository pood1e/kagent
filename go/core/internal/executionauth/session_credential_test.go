package executionauth

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func TestSessionCredentialExpiryIsolationAndLiveSource(t *testing.T) {
	f := newCredentialFixture(t)
	registered, err := f.service.CredentialStatus(f.caller)
	require.NoError(t, err)
	require.False(t, registered)
	require.NoError(t, f.service.RegisterCredential(f.caller, "refresh-token"))
	registered, err = f.service.CredentialStatus(f.caller)
	require.NoError(t, err)
	require.True(t, registered)
	issued, err := f.service.IssueSessionCredential(f.caller, &apiv1.IssueSessionCredentialRequest{SessionId: f.store.grant.SessionId, Ttl: durationpb.New(time.Minute)})
	require.NoError(t, err)
	provider := &Provider{Users: f.users, Authority: f.service}
	headers := http.Header{"Authorization": {"Bearer " + issued.Credential}}
	bound, err := provider.Authenticate(t.Context(), headers, nil)
	require.NoError(t, err)
	require.Equal(t, f.store.grant.InitiatorId, bound.Principal().ID)
	require.Equal(t, []string{"read", "write"}, bound.Principal().Scopes)
	require.Error(t, f.service.RegisterCredential(auth.AuthSessionTo(t.Context(), bound), "refresh-token"))
	f.roles.Store([]string{"read", "admin"})
	f.expire(t)
	bound, err = provider.Authenticate(t.Context(), headers, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, bound.Principal().Scopes)
	past := proto.CloneOf(f.store.grant)
	past.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
	payload, err := proto.Marshal(past)
	require.NoError(t, err)
	for _, ciphertext := range [][]byte{
		f.service.cipher.Seal(nil, nil, payload, sessionCredentialPurpose),
		f.service.cipher.Seal(nil, nil, payload, []byte(f.store.grant.InitiatorId)),
	} {
		_, err := f.service.authenticateSessionCredential(t.Context(), base64.RawURLEncoding.EncodeToString(ciphertext))
		require.Error(t, err)
	}
	_, err = provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + issued.Credential, "Bearer " + issued.Credential}}, nil)
	require.Error(t, err)
	f.store.active = false
	_, err = bound.(auth.TokenSource).Token(t.Context())
	require.Error(t, err)
	_, err = provider.Authenticate(t.Context(), headers, nil)
	require.Error(t, err)
}

func TestSessionCredentialRequiresExplicitScopeCeiling(t *testing.T) {
	f := newCredentialFixture(t)
	grant := proto.CloneOf(f.store.grant)
	grant.ScopeCeiling = nil
	grant.ExpiresAt = timestamppb.New(time.Now().Add(time.Minute))
	payload, err := proto.Marshal(grant)
	require.NoError(t, err)
	sealed := f.service.cipher.Seal(nil, nil, payload, sessionCredentialPurpose)
	_, err = f.service.authenticateSessionCredential(t.Context(), base64.RawURLEncoding.EncodeToString(sealed))
	require.Error(t, err)
}
