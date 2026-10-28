package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	pkgauth "github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func TestOIDCVerifiesUserCredentialsAndRotatesSigningKeys(t *testing.T) {
	issuer := jwttest.New(t)
	cfg := &configv1.OIDCAuthentication{Issuer: issuer.URL, Audience: "kagent-api", CaFile: issuer.CAFile}
	provider, err := NewOIDCAuthenticator(t.Context(), cfg)
	require.NoError(t, err)
	claims := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": issuer.URL, "aud": "kagent-api", "sub": "alice", "email": "display@example.com", "exp": time.Now().Add(time.Hour).Unix(), "roles": []string{"write"}}
	}
	valid := issuer.Token(t, claims())
	caller, err := provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + valid}, "X-User-Id": {"admin"}}, nil)
	require.NoError(t, err)
	require.Equal(t, pkgauth.PrincipalID("oidc:"+issuer.URL, "alice"), caller.Principal().ID)
	require.Equal(t, []any{"write"}, caller.Principal().Claims["roles"])
	raw, err := caller.(pkgauth.TokenSource).Token(t.Context())
	require.NoError(t, err)
	require.Equal(t, valid, raw)
	for _, tc := range []struct {
		name, claim string
		value       any
	}{
		{"issuer", "iss", "https://attacker.example"},
		{"audience", "aud", "other-api"},
		{"expired", "exp", time.Now().Add(-time.Minute).Unix()},
		{"missing expiry", "exp", nil},
		{"future validity", "nbf", time.Now().Add(time.Hour).Unix()},
		{"missing subject", "sub", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := claims()
			c[tc.claim] = tc.value
			_, err := provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + issuer.Token(t, c)}}, nil)
			require.ErrorIs(t, err, ErrUnauthenticated)
		})
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims()).SignedString([]byte("attacker-key"))
	require.NoError(t, err)
	for _, headers := range []http.Header{
		{}, {"X-User-Id": {"admin"}}, {"Authorization": {"Bearer " + forged}},
		{"Authorization": {"Bearer " + valid, "Bearer " + valid}},
	} {
		_, err := provider.Authenticate(t.Context(), headers, nil)
		require.ErrorIs(t, err, ErrUnauthenticated)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims())
	token.Header["kid"] = "new-key"
	newRaw, err := token.SignedString(otherKey)
	require.NoError(t, err)
	_, err = provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + newRaw}}, nil)
	require.ErrorIs(t, err, ErrUnauthenticated)
	issuer.SetKey(t, &otherKey.PublicKey, "new-key")
	_, err = provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + newRaw}}, nil)
	require.NoError(t, err)
	untrusted := proto.CloneOf(cfg)
	untrusted.CaFile = ""
	_, err = NewOIDCAuthenticator(t.Context(), untrusted)
	require.Error(t, err)
}
