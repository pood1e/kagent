package executionauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/kagent-dev/kagent/go/pkg/logging"
)

// assertionLifetime bounds each single-use RFC 7523 assertion.
const assertionLifetime = time.Minute

type assertionKey struct {
	KeyType string `json:"kty"`
	Curve   string `json:"crv"`
	Alg     string `json:"alg"`
	Use     string `json:"use"`
	KeyID   string `json:"kid"`
	X       string `json:"x"`
}

type assertionKeySet struct {
	Keys []assertionKey `json:"keys"`
}

// assertionClaims keeps aud single-valued, the form identity providers accept
// for JWT bearer grants.
type assertionClaims struct {
	Audience string `json:"aud"`
	jwt.RegisteredClaims
}

// assertionSigner names registered principals in JWT bearer assertions and
// publishes the key the identity provider verifies them with.
type assertionSigner struct {
	issuer string
	keyID  string
	key    ed25519.PrivateKey
	keys   []byte
}

var _ http.Handler = (*assertionSigner)(nil)

func newAssertionSigner(issuer, keyFile string) (*assertionSigner, error) {
	material, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read delegation assertion key: %w", err)
	}
	defer clear(material)
	block, rest := pem.Decode(bytes.TrimSpace(material))
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("delegation assertion key requires one unencrypted PKCS#8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, ok := parsed.(ed25519.PrivateKey)
	if err != nil || !ok {
		return nil, fmt.Errorf("delegation assertion key requires Ed25519")
	}
	public := key.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(public)
	signer := &assertionSigner{issuer: issuer, keyID: base64.RawURLEncoding.EncodeToString(digest[:]), key: key}
	signer.keys, err = json.Marshal(assertionKeySet{Keys: []assertionKey{{
		KeyType: "OKP", Curve: "Ed25519", Alg: "EdDSA", Use: "sig",
		KeyID: signer.keyID, X: base64.RawURLEncoding.EncodeToString(public),
	}}})
	if err != nil {
		return nil, fmt.Errorf("encode delegation assertion keys: %w", err)
	}
	return signer, nil
}

// sign binds one subject to the token endpoint for a single exchange.
func (s *assertionSigner) sign(subject, audience string) (string, error) {
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, assertionClaims{
		Audience: audience,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(assertionLifetime)),
			ID:        uuid.NewString(),
		},
	})
	token.Header["kid"] = s.keyID
	return token.SignedString(s.key)
}

// ServeHTTP exposes only the public verification key.
func (s *assertionSigner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/jwk-set+json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if _, err := w.Write(s.keys); err != nil {
		logging.FromContext(r.Context()).ErrorContext(r.Context(), "failed to write delegation assertion keys", "error", err)
	}
}
