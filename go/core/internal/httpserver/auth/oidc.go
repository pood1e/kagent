package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"github.com/coreos/go-oidc/v3/oidc"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// NewAuthenticator selects the configured user credential verifier. Actor and
// Kubernetes credentials have their own independently verified providers.
func NewAuthenticator(ctx context.Context, cfg *configv1.AuthenticationConfig) (auth.AuthProvider, error) {
	switch cfg.GetMode() {
	case "insecure":
		return &InsecureAuthenticator{}, nil
	case "trusted-proxy":
		return NewProxyAuthenticator(cfg.GetUserIdClaim()), nil
	case "oidc":
		return NewOIDCAuthenticator(ctx, cfg.GetOidc())
	default:
		return nil, fmt.Errorf("unsupported server.auth.mode %q", cfg.GetMode())
	}
}

// OIDCAuthenticator verifies the JWT signature, issuer, audience and lifetime.
// Identity uses the issuer and subject; mutable display claims remain attributes.
type OIDCAuthenticator struct {
	verifier *oidc.IDTokenVerifier
	issuer   string
}

var _ auth.AuthProvider = (*OIDCAuthenticator)(nil)

func NewOIDCAuthenticator(ctx context.Context, cfg *configv1.OIDCAuthentication) (*OIDCAuthenticator, error) {
	if cfg == nil {
		return nil, fmt.Errorf("server.auth.oidc configuration is required")
	}
	if err := protovalidate.Validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid OIDC authentication configuration: %w", err)
	}
	if !httpsEndpoint(cfg.Issuer) {
		return nil, fmt.Errorf("OIDC issuer requires an HTTPS URL without credentials, query, or fragment")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CaFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load OIDC system CA roots: %w", err)
		}
		pem, err := os.ReadFile(cfg.CaFile)
		if err != nil {
			return nil, fmt.Errorf("read OIDC CA file: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("OIDC CA file contains no certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	clientContext := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(clientContext, cfg.Issuer)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	var metadata struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil || !httpsEndpoint(metadata.JWKSURI) {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("OIDC signing keys require an HTTPS URL without credentials, query, or fragment")
	}
	context.AfterFunc(ctx, transport.CloseIdleConnections)
	return &OIDCAuthenticator{issuer: cfg.Issuer, verifier: provider.VerifierContext(clientContext, &oidc.Config{ClientID: cfg.Audience})}, nil
}

func httpsEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && !strings.Contains(raw, "#")
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, headers http.Header, _ url.Values) (auth.Session, error) {
	if len(headers.Values("Authorization")) != 1 {
		return nil, ErrUnauthenticated
	}
	scheme, raw, ok := strings.Cut(headers.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || raw == "" {
		return nil, ErrUnauthenticated
	}
	token, err := a.verifier.Verify(ctx, raw)
	if err != nil || token.Subject == "" {
		return nil, ErrUnauthenticated
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return nil, ErrUnauthenticated
	}
	return &SimpleSession{
		P:                auth.Principal{ID: auth.PrincipalID("oidc:"+a.issuer, token.Subject), Claims: claims},
		bearerCredential: bearerCredential{authHeader: "Bearer " + raw, expiresAt: token.Expiry},
	}, nil
}

func (a *OIDCAuthenticator) UpstreamAuth(r *http.Request, session auth.Session, _ string) error {
	return auth.ForwardCredential(r, session)
}
