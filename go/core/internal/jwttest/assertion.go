package jwttest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/stretchr/testify/require"
)

// AssertionKey writes an unencrypted PKCS#8 Ed25519 delegation assertion key.
func AssertionKey(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "assertion-key.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))
	return path
}

// PublishedKeys reads the JWK set a verification key handler serves.
func PublishedKeys(t *testing.T, handler http.Handler) []byte {
	t.Helper()
	require.NotNil(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/delegation/jwks.json", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "application/jwk-set+json", response.Header().Get("Content-Type"))
	return response.Body.Bytes()
}

// Assertion is a verified RFC 7523 JWT bearer assertion.
type Assertion struct {
	Subject string
	ID      string
}

// VerifyAssertion applies the identity provider checks for a JWT bearer grant:
// a published EdDSA key, the exact issuer and single audience, and a short lifetime.
func VerifyAssertion(keys []byte, raw, issuer, audience string) (Assertion, error) {
	set, err := jwk.Parse(keys)
	if err != nil {
		return Assertion{}, err
	}
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		key, ok := set.LookupKeyID(kid)
		if !ok {
			return nil, fmt.Errorf("unknown assertion key")
		}
		var public ed25519.PublicKey
		if err := key.Raw(&public); err != nil {
			return nil, err
		}
		return public, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(issuer), jwt.WithAudience(audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return Assertion{}, err
	}
	if _, single := claims["aud"].(string); !single {
		return Assertion{}, fmt.Errorf("assertion audience must be single-valued")
	}
	issued, _ := claims.GetIssuedAt()
	expires, _ := claims.GetExpirationTime()
	if issued == nil || expires.Sub(issued.Time) > 5*time.Minute {
		return Assertion{}, fmt.Errorf("assertion lifetime exceeds the identity provider limit")
	}
	subject, _ := claims.GetSubject()
	id, _ := claims["jti"].(string)
	if subject == "" || id == "" {
		return Assertion{}, fmt.Errorf("assertion requires sub and jti")
	}
	return Assertion{Subject: subject, ID: id}, nil
}
