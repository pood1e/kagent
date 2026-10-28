package grpcserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

const exchangeAudience = "https://environment.example.test/mcp"
const accessTokenType = "urn:ietf:params:oauth:token-type:access_token"

type authorityOAuthIssuer struct {
	identity             *jwttest.Issuer
	users                auth.AuthProvider
	endpoint             *httptest.Server
	ca, key, secret      string
	refreshes, exchanges atomic.Int32
	firstExpiry          atomic.Int64
}

func newAuthorityOAuthIssuer(t *testing.T) *authorityOAuthIssuer {
	t.Helper()
	f := &authorityOAuthIssuer{identity: jwttest.New(t)}
	var err error
	f.users, err = authimpl.NewOIDCAuthenticator(t.Context(), &configv1.OIDCAuthentication{Issuer: f.identity.URL, Audience: "kagent-api", CaFile: f.identity.CAFile})
	require.NoError(t, err)
	directory := t.TempDir()
	f.ca, f.key, f.secret = filepath.Join(directory, "ca.pem"), filepath.Join(directory, "key"), filepath.Join(directory, "client-secret")
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.key, key, 0o600))
	require.NoError(t, os.WriteFile(f.secret, []byte("native-client-secret"), 0o600))
	f.endpoint = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id, secret, ok := r.BasicAuth()
		if !ok || r.ParseForm() != nil {
			http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		switch r.Form.Get("grant_type") {
		case "refresh_token":
			if id != "native-client" || secret != "native-client-secret" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			before := f.refreshes.Load()
			expected := "initial-refresh"
			if before > 0 {
				expected = fmt.Sprintf("rotated-refresh-%d", before)
			}
			if r.Form.Get("refresh_token") != expected {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			generation := f.refreshes.Add(1)
			lifetime := 3600
			if generation == 1 {
				lifetime = 45
				f.firstExpiry.Store(time.Now().Add(45 * time.Second).Unix())
			}
			token := f.userToken(t, fmt.Sprintf("renewed-%d", generation), time.Duration(lifetime)*time.Second)
			assert.NoError(t, json.NewEncoder(w).Encode(struct {
				AccessToken  string `json:"access_token"`
				TokenType    string `json:"token_type"`
				RefreshToken string `json:"refresh_token"`
				ExpiresIn    int    `json:"expires_in"`
			}{token, "Bearer", fmt.Sprintf("rotated-refresh-%d", generation), lifetime}))
		case "urn:ietf:params:oauth:grant-type:token-exchange":
			if id != "environment-client" || secret != "environment-client-secret" ||
				r.Form.Get("audience") != exchangeAudience || r.Form.Get("scope") != "environment-read" ||
				r.Form.Get("subject_token_type") != accessTokenType || r.Form.Get("requested_token_type") != accessTokenType {
				http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
				return
			}
			caller, err := f.users.Authenticate(r.Context(), http.Header{"Authorization": {"Bearer " + r.Form.Get("subject_token")}}, nil)
			if err != nil || caller.Principal().ID != auth.PrincipalID("oidc:"+f.identity.URL, "alice") {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			generation, ok := caller.Principal().Claims["jti"].(string)
			// Only the native refresh grant's signed IdP token is accepted. Login tokens,
			// Actor JWTs and machine credentials are distinct, rejected token families.
			if !ok || !strings.HasPrefix(generation, "renewed-") {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			f.exchanges.Add(1)
			assert.NoError(t, json.NewEncoder(w).Encode(struct {
				AccessToken     string `json:"access_token"`
				TokenType       string `json:"token_type"`
				IssuedTokenType string `json:"issued_token_type"`
				ExpiresIn       int    `json:"expires_in"`
			}{"environment-alice-" + generation, "Bearer", accessTokenType, 3600}))
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		}
	}))
	certificate, root := authorityExchangeCertificate(t)
	f.endpoint.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	f.endpoint.StartTLS()
	t.Cleanup(f.endpoint.Close)
	require.NoError(t, os.WriteFile(f.ca, root, 0o600))
	return f
}

func (f *authorityOAuthIssuer) userToken(t *testing.T, id string, lifetime time.Duration) string {
	t.Helper()
	return f.identity.Token(t, jwt.MapClaims{"iss": f.identity.URL, "aud": "kagent-api", "sub": "alice", "roles": []string{"read", "write"}, "jti": id, "exp": time.Now().Add(lifetime).Unix()})
}

func (f *authorityOAuthIssuer) configuration() *configv1.ExecutionAuthorizationConfig {
	return &configv1.ExecutionAuthorizationConfig{
		ScopeClaim: "roles", EncryptionKeyFile: f.key,
		Rules: []*configv1.ResourceScopeRule{
			{ResourceType: "Session", Verbs: []string{"get"}},
			{ResourceType: "Session", Verbs: []string{"create", "update"}, AnyScope: []string{"write"}},
			{ResourceType: "Agent", Verbs: []string{"get", "list"}},
			{ResourceType: "TaskStore", Verbs: []string{"get", "list", "create", "update"}},
		},
		Oauth: &configv1.OAuthCredentialConfig{TokenEndpoint: f.endpoint.URL + "/token", ClientId: "native-client", ClientSecretFile: f.secret, CaFile: f.ca},
	}
}

func (f *authorityOAuthIssuer) waitForRefresh(t *testing.T) {
	t.Helper()
	// Exercise the real OAuth expiry boundary without modifying encrypted storage.
	timer := time.NewTimer(time.Until(time.Unix(f.firstExpiry.Load(), 0).Add(-29 * time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(context.Cause(t.Context()))
	}
}

func authorityExchangeCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "native OAuth test root"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "native OAuth test endpoint"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, rootKey)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
}
