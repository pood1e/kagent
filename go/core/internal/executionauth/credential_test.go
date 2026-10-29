package executionauth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/dbtest"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type credentialStore struct {
	*database.Client
	grant  *apiv1.SessionAuthority
	active bool
}

func (s *credentialStore) GetSessionAuthority(context.Context, string) (*apiv1.SessionAuthority, bool, error) {
	return proto.CloneOf(s.grant), s.active, nil
}

type credentialFixture struct {
	identity     *jwttest.Issuer
	users        auth.AuthProvider
	store        *credentialStore
	service      *Service
	caller       context.Context
	config       *configv1.ExecutionAuthorizationConfig
	requests     atomic.Int32
	mode         atomic.Int32
	roles        atomic.Value
	clientSecret atomic.Value
}

func (f *credentialFixture) accessToken(t *testing.T, subject string, roles []string) string {
	t.Helper()
	return f.identity.Token(t, jwt.MapClaims{"iss": f.identity.URL, "aud": "kagent", "sub": subject, "exp": time.Now().Add(time.Hour).Unix(), "roles": roles})
}

func newCredentialFixture(t *testing.T) *credentialFixture {
	t.Helper()
	dsn := dbtest.StartT(t.Context(), t)
	dbtest.MigrateT(t, dsn, false)
	pool, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	identity := jwttest.New(t)
	users, err := authimpl.NewOIDCAuthenticator(t.Context(), &configv1.OIDCAuthentication{Issuer: identity.URL, Audience: "kagent", CaFile: identity.CAFile})
	require.NoError(t, err)
	principal := auth.PrincipalID("oidc:"+identity.URL, "alice")
	fixture := &credentialFixture{identity: identity, users: users, store: &credentialStore{Client: database.NewClient(pool, "public"), grant: &apiv1.SessionAuthority{SessionId: uuid.NewString(), InitiatorId: principal, RootSessionId: uuid.NewString(), ScopeCeiling: &apiv1.ScopeCeiling{Scopes: []string{"read", "write"}}}, active: true}}
	fixture.store.grant.RootSessionId = fixture.store.grant.SessionId
	fixture.roles.Store([]string{"read", "write", "admin"})
	fixture.clientSecret.Store("client-secret-1")
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.requests.Add(1)
		id, secret, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "kagent", id)
		require.Equal(t, fixture.clientSecret.Load(), secret)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		w.Header().Set("Content-Type", "application/json")
		switch fixture.mode.Load() {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, err := w.Write([]byte(`{"error":"invalid_grant","error_description":"secret-refresh-token"}`))
			require.NoError(t, err)
			return
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, err := w.Write([]byte(`{"error":"temporarily_unavailable"}`))
			require.NoError(t, err)
			return
		}
		subject := "alice"
		if r.Form.Get("refresh_token") == "wrong-subject" {
			subject = "bob"
		}
		require.NoError(t, json.NewEncoder(w).Encode(struct {
			AccessToken  string `json:"access_token"`
			TokenType    string `json:"token_type"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}{fixture.accessToken(t, subject, fixture.roles.Load().([]string)), "Bearer", "rotated-refresh-token", 3600}))
	}))
	t.Cleanup(endpoint.Close)
	directory := t.TempDir()
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	keyFile, caFile, secretFile := filepath.Join(directory, "key"), filepath.Join(directory, "ca.pem"), filepath.Join(directory, "client-secret")
	require.NoError(t, os.WriteFile(keyFile, key, 0o600))
	require.NoError(t, os.WriteFile(secretFile, []byte("client-secret-1"), 0o600))
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: endpoint.Certificate().Raw}), 0o600))
	fixture.config = &configv1.ExecutionAuthorizationConfig{ScopeClaim: "roles", EncryptionKeyFile: keyFile, Rules: []*configv1.ResourceScopeRule{{ResourceType: "Session", Verbs: []string{"get", "create", "update"}}}, Oauth: &configv1.OAuthCredentialConfig{TokenEndpoint: endpoint.URL, ClientId: "kagent", ClientSecretFile: secretFile, CaFile: caFile}}
	fixture.service, err = NewService(fixture.store, fixture.users, fixture.config, false)
	require.NoError(t, err)
	provider := &Provider{Users: fixture.service.users, Authority: fixture.service}
	caller, err := provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + fixture.accessToken(t, "alice", []string{"read", "write"})}}, nil)
	require.NoError(t, err)
	fixture.caller = auth.AuthSessionTo(t.Context(), caller)
	return fixture
}

func (f *credentialFixture) expire(t *testing.T) {
	t.Helper()
	row, err := f.store.GetDelegationCredential(t.Context(), f.store.grant.InitiatorId)
	require.NoError(t, err)
	token, err := f.service.open(f.store.grant.InitiatorId, row.Ciphertext)
	require.NoError(t, err)
	token.Expiry = time.Now().Add(-time.Minute)
	ciphertext, err := f.service.seal(f.store.grant.InitiatorId, token)
	require.NoError(t, err)
	require.NoError(t, f.store.SaveDelegationCredential(t.Context(), f.store.grant.InitiatorId, ciphertext))
}

func TestDurableSourceRefreshesOnceAcrossReplicasAndNarrowsLiveScopes(t *testing.T) {
	f := newCredentialFixture(t)
	require.NoError(t, f.service.RegisterCredential(f.caller, "initial-refresh-token"))
	row, err := f.store.GetDelegationCredential(t.Context(), f.store.grant.InitiatorId)
	require.NoError(t, err)
	require.NotContains(t, string(row.Ciphertext), "refresh-token")
	require.NotContains(t, string(row.Ciphertext), "Bearer")
	_, err = f.service.open("another-principal", row.Ciphertext)
	require.Error(t, err)
	renewed, err := f.service.open(f.store.grant.InitiatorId, row.Ciphertext)
	require.NoError(t, err)
	require.Equal(t, "rotated-refresh-token", renewed.RefreshToken)
	restarted, err := NewService(f.store, f.users, f.config, false)
	require.NoError(t, err)
	f.expire(t)
	f.roles.Store([]string{"read", "admin"})
	f.clientSecret.Store("client-secret-2")
	require.NoError(t, os.WriteFile(f.config.Oauth.ClientSecretFile, []byte("client-secret-2"), 0o600))
	before := f.requests.Load()
	var group sync.WaitGroup
	failures := make(chan error, 16)
	for index := range 16 {
		service := f.service
		if index%2 == 0 {
			service = restarted
		}
		group.Go(func() {
			caller, err := service.Resolve(t.Context(), f.store.grant.SessionId)
			if err != nil {
				failures <- err
				return
			}
			if !strings.EqualFold(strings.Join(caller.Principal().Scopes, ","), "read") {
				failures <- context.Canceled
				return
			}
			raw, err := caller.(auth.TokenSource).Token(t.Context())
			if err != nil || raw == "" {
				failures <- context.Canceled
			}
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), f.requests.Load()-before, "one persisted refresh lease must serve both replicas")
	resolved, err := restarted.Resolve(t.Context(), f.store.grant.SessionId)
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, resolved.Principal().Claims["roles"])
	require.Equal(t, f.store.grant.InitiatorId, resolved.Principal().ID)
	require.Equal(t, f.store.grant, resolved.Principal().Execution)
	f.store.active = false
	_, err = resolved.(auth.TokenSource).Token(t.Context())
	require.Error(t, err)
}

func TestCredentialBindingFailuresNeverSelectAnotherSource(t *testing.T) {
	f := newCredentialFixture(t)
	require.NoError(t, f.service.RegisterCredential(f.caller, "initial"))
	before, err := f.store.GetDelegationCredential(t.Context(), f.store.grant.InitiatorId)
	require.NoError(t, err)
	require.Error(t, f.service.RegisterCredential(f.caller, "wrong-subject"))
	after, err := f.store.GetDelegationCredential(t.Context(), f.store.grant.InitiatorId)
	require.NoError(t, err)
	require.Equal(t, before, after)
	f.expire(t)
	f.mode.Store(2)
	_, err = f.service.Resolve(t.Context(), f.store.grant.SessionId)
	require.Error(t, err)
	f.mode.Store(0)
	_, err = f.service.Resolve(t.Context(), f.store.grant.SessionId)
	require.NoError(t, err)
	f.expire(t)
	f.mode.Store(1)
	_, err = f.service.Resolve(t.Context(), f.store.grant.SessionId)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-refresh-token")
	_, err = f.store.GetDelegationCredential(t.Context(), f.store.grant.InitiatorId)
	require.ErrorIs(t, err, database.ErrNotFound)
	require.Error(t, f.service.RevokeCredential(auth.AuthSessionTo(t.Context(), auth.NewCredentialSession(auth.Principal{ID: f.store.grant.InitiatorId, RuntimeBinding: &auth.RuntimeBinding{SessionID: f.store.grant.SessionId}}, nil))))
	noTrust := proto.CloneOf(f.config)
	noTrust.Oauth.CaFile = ""
	untrusted, err := NewService(f.store, f.users, noTrust, false)
	require.NoError(t, err)
	require.Error(t, untrusted.RegisterCredential(f.caller, "initial"))
}

func TestNonrenewableSourceHasBoundedAuthorityAndNoFallback(t *testing.T) {
	f := newCredentialFixture(t)
	grant, err := f.service.Bind(f.caller, uuid.NewString(), f.store.grant.InitiatorId, &apiv1.ScopeCeiling{Scopes: []string{"read", "admin"}})
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, grant.ScopeCeiling.Scopes)
	require.NotNil(t, grant.ExpiresAt)
	f.store.grant = grant
	resolved, err := f.service.Resolve(t.Context(), grant.SessionId)
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, resolved.Principal().Scopes)
	f.expire(t)
	_, err = f.service.Resolve(t.Context(), grant.SessionId)
	require.Error(t, err)
	require.NoError(t, f.service.RevokeCredential(f.caller))
	_, err = resolved.(auth.TokenSource).Token(t.Context())
	require.Error(t, err)
	secure, err := NewService(f.store, f.users, nil, false)
	require.NoError(t, err)
	_, err = secure.Resolve(t.Context(), grant.SessionId)
	require.Error(t, err)
}

func TestNonrenewableSourceRotatesOnlyForItsVerifiedInitiator(t *testing.T) {
	f := newCredentialFixture(t)
	grant, err := f.service.Bind(f.caller, uuid.NewString(), f.store.grant.InitiatorId, nil)
	require.NoError(t, err)
	f.store.grant = grant
	initial, err := f.store.GetDelegationCredential(t.Context(), grant.InitiatorId)
	require.NoError(t, err)
	f.expire(t)
	_, err = f.service.Resolve(t.Context(), grant.SessionId)
	require.Error(t, err)
	newToken := f.identity.Token(t, jwt.MapClaims{"iss": f.identity.URL, "aud": "kagent", "sub": "alice", "exp": time.Now().Add(2 * time.Hour).Unix(), "roles": []string{"read"}})
	provider := &Provider{Users: f.users, Authority: f.service}
	newCaller, err := provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + newToken}}, nil)
	require.NoError(t, err)
	require.NoError(t, f.service.Renew(auth.AuthSessionTo(t.Context(), newCaller)))
	row, err := f.store.GetDelegationCredential(t.Context(), grant.InitiatorId)
	require.NoError(t, err)
	require.Greater(t, row.Version, initial.Version)
	stored, err := f.service.open(grant.InitiatorId, row.Ciphertext)
	require.NoError(t, err)
	require.Equal(t, newToken, stored.AccessToken)
	require.NoError(t, f.service.Renew(f.caller))
	unchanged, err := f.store.GetDelegationCredential(t.Context(), grant.InitiatorId)
	require.NoError(t, err)
	require.Equal(t, row, unchanged)
	resolved, err := f.service.Resolve(t.Context(), grant.SessionId)
	require.NoError(t, err)
	require.Equal(t, []string{"read"}, resolved.Principal().Scopes)
	stranger, err := provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + f.accessToken(t, "bob", []string{"admin"})}}, nil)
	require.NoError(t, err)
	require.NoError(t, f.service.Renew(auth.AuthSessionTo(t.Context(), stranger)))
	after, err := f.store.GetDelegationCredential(t.Context(), grant.InitiatorId)
	require.NoError(t, err)
	require.Equal(t, row, after)
}
