// Package executionauth owns durable execution authority and renewable caller
// credentials. Actor verification and transport remain separate boundaries.
package executionauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"buf.build/go/protovalidate"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type Store interface {
	GetSessionAuthority(context.Context, string) (*apiv1.SessionAuthority, bool, error)
	GetSessionByID(context.Context, string) (*apiv1.Session, error)
	RevokeSessionAuthority(context.Context, string) error
	GetDelegationCredential(context.Context, string) (*database.DelegationCredential, error)
	SaveDelegationCredential(context.Context, string, []byte) error
	CaptureDelegationCredential(context.Context, string, int64, []byte, time.Time) (bool, error)
	ClaimDelegationCredential(context.Context, string, int64, uuid.UUID) (bool, error)
	FinishDelegationCredential(context.Context, string, int64, uuid.UUID, []byte) (bool, error)
	InvalidateDelegationCredential(context.Context, string, int64, uuid.UUID) error
	DeleteDelegationCredential(context.Context, string) error
}

type Service struct {
	store    Store
	users    auth.AuthProvider
	config   *configv1.ExecutionAuthorizationConfig
	cipher   cipher.AEAD
	http     *http.Client
	insecure bool
}

// NewService validates vault settings once. A nil config retains the explicitly
// configured OSS policy and source-less identity, while preserving native fences.
func NewService(store Store, users auth.AuthProvider, config *configv1.ExecutionAuthorizationConfig, insecure bool) (*Service, error) {
	service := &Service{insecure: insecure, store: store, users: users, config: config, http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if config == nil {
		return service, nil
	}
	if err := protovalidate.Validate(config); err != nil {
		return nil, fmt.Errorf("invalid execution authorization configuration: %w", err)
	}
	key, err := os.ReadFile(config.EncryptionKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read execution credential encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("execution credential encryption key requires 32 bytes")
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, fmt.Errorf("configure execution credential cipher: %w", err)
	}
	service.cipher, err = cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("configure execution credential cipher: %w", err)
	}
	if oauth := config.GetOauth(); oauth != nil {
		endpoint, err := url.Parse(oauth.TokenEndpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
			return nil, fmt.Errorf("OAuth credential endpoint requires an HTTPS URL")
		}
	}
	if err := service.configureOAuthTLS(); err != nil {
		return nil, err
	}
	return service, nil
}

// Normalize projects authenticated IdP scope attributes into a typed principal.
func (s *Service) Normalize(principal auth.Principal) (auth.Principal, error) {
	if s.config == nil {
		return principal, nil
	}
	var scopes []string
	switch claim := principal.Claims[s.config.ScopeClaim].(type) {
	case nil:
	case string:
		scopes = strings.Fields(claim)
	case []string:
		scopes = slices.Clone(claim)
	case []any:
		for _, value := range claim {
			text, ok := value.(string)
			if !ok {
				return auth.Principal{}, fmt.Errorf("invalid authenticated scope claim")
			}
			scopes = append(scopes, text)
		}
	default:
		return auth.Principal{}, fmt.Errorf("invalid authenticated scope claim")
	}
	if len(scopes) > 256 {
		return auth.Principal{}, fmt.Errorf("too many authenticated scopes")
	}
	for _, scope := range scopes {
		if len(scope) == 0 || len(scope) > 256 || strings.ContainsFunc(scope, unicode.IsSpace) {
			return auth.Principal{}, fmt.Errorf("invalid authenticated scope")
		}
	}
	slices.Sort(scopes)
	principal.Scopes = slices.Compact(scopes)
	return principal, nil
}

func intersect(scopes, ceiling []string) []string {
	result := slices.Clone(scopes)
	return slices.DeleteFunc(result, func(scope string) bool { return !slices.Contains(ceiling, scope) })
}

// Bind captures the authenticated initiator and immutable scope ceiling. Runtime
// parentage comes exclusively from the verified binding. The store rechecks the
// parent fence in the creation transaction.
func (s *Service) Bind(ctx context.Context, id, creator string, ceiling *apiv1.ScopeCeiling) (*apiv1.SessionAuthority, error) {
	if ceiling != nil {
		if err := protovalidate.Validate(ceiling); err != nil {
			return nil, err
		}
	}
	if s.config == nil && !s.insecure {
		return nil, fmt.Errorf("durable execution credentials are not configured")
	}
	caller, ok := auth.AuthSessionFrom(ctx)
	if !ok || caller.Principal().ID == "" {
		return nil, fmt.Errorf("execution initiator is required")
	}
	principal, err := s.Normalize(caller.Principal())
	if err != nil {
		return nil, err
	}
	grant := &apiv1.SessionAuthority{SessionId: id, InitiatorId: principal.ID, RootSessionId: id, ScopeCeiling: &apiv1.ScopeCeiling{Scopes: slices.Clone(principal.Scopes)}}
	if binding := principal.RuntimeBinding; binding != nil {
		parent, err := s.Resolve(ctx, binding.SessionID)
		if err != nil {
			return nil, err
		}
		authority := parent.Principal().Execution
		owner, err := s.store.GetSessionByID(ctx, binding.SessionID)
		if err != nil {
			return nil, err
		}
		if creator != owner.Creator {
			return nil, fmt.Errorf("execution storage owner mismatch")
		}
		grant.InitiatorId, grant.ParentSessionId, grant.RootSessionId = authority.InitiatorId, binding.SessionID, authority.RootSessionId
		grant.ScopeCeiling.Scopes = slices.Clone(parent.Principal().Scopes)
		// A forwarded human credential may narrow its originating runtime further.
		if principal.ID == authority.InitiatorId {
			grant.ScopeCeiling.Scopes = intersect(grant.ScopeCeiling.Scopes, principal.Scopes)
		}
		grant.ExpiresAt = authority.ExpiresAt
	} else if s.config != nil {
		renewable, expiry, err := s.capture(ctx, caller, true)
		if err != nil {
			return nil, err
		}
		if !renewable {
			grant.ExpiresAt = timestamppb.New(expiry)
		}
	}
	if ceiling != nil {
		grant.ScopeCeiling.Scopes = intersect(grant.ScopeCeiling.Scopes, ceiling.Scopes)
	}
	return grant, nil
}

// Renew captures a newer verified bearer for an existing initiator. It never
// recreates a revoked credential or widens an immutable scope ceiling.
func (s *Service) Renew(ctx context.Context) error {
	if s.config == nil {
		return nil
	}
	if _, err := credentialOwner(ctx); err != nil {
		return err
	}
	caller, _ := auth.AuthSessionFrom(ctx)
	_, _, err := s.capture(ctx, caller, false)
	return err
}

// Resolve reconstructs a narrowed initiator and live TokenSource after restarts.
// Every resolution applies ancestor revocation/expiry and current credentials.
func (s *Service) Resolve(ctx context.Context, id string) (auth.Session, error) {
	grant, active, err := s.store.GetSessionAuthority(ctx, id)
	if err != nil {
		return nil, err
	}
	if !active || grant.ScopeCeiling == nil {
		return nil, fmt.Errorf("execution authority revoked, expired, or unbounded")
	}
	principal := auth.Principal{ID: grant.InitiatorId, Scopes: slices.Clone(grant.ScopeCeiling.Scopes), Execution: grant}
	if s.config == nil {
		if !s.insecure {
			return nil, fmt.Errorf("durable execution credentials are not configured")
		}
		return auth.NewCredentialSession(principal, nil), nil
	}
	credential, current, err := s.current(ctx, grant.InitiatorId)
	if err != nil {
		return nil, err
	}
	principal.Claims = maps.Clone(current.Claims)
	principal.Scopes = intersect(principal.Scopes, current.Scopes)
	principal.Claims[s.config.ScopeClaim] = slices.Clone(principal.Scopes)
	if !credential.Valid() {
		return nil, fmt.Errorf("execution credential expired")
	}
	return auth.NewCredentialSession(principal, &source{service: s, sessionID: id}), nil
}

type source struct {
	service   *Service
	sessionID string
}

var _ auth.TokenSource = (*source)(nil)

func (s *source) Token(ctx context.Context) (string, error) {
	grant, active, err := s.service.store.GetSessionAuthority(ctx, s.sessionID)
	if err != nil {
		return "", err
	}
	if !active {
		return "", fmt.Errorf("execution authority revoked or expired")
	}
	token, _, err := s.service.current(ctx, grant.InitiatorId)
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

// Provider adds typed scopes without changing authentication or credential trust.
type Provider struct {
	Users     auth.AuthProvider
	Authority *Service
}

var _ auth.AuthProvider = (*Provider)(nil)

func (p *Provider) Authenticate(ctx context.Context, headers http.Header, query url.Values) (auth.Session, error) {
	if credential, ok := sessionCredential(headers); ok {
		return p.Authority.authenticateSessionCredential(ctx, credential)
	}
	session, err := p.Users.Authenticate(ctx, headers, query)
	if err != nil || session == nil {
		return nil, err
	}
	principal, err := p.Authority.Normalize(session.Principal())
	if err != nil {
		return nil, err
	}
	token, _ := session.(auth.TokenSource)
	return auth.NewCredentialSession(principal, token), nil
}
func (p *Provider) UpstreamAuth(request *http.Request, session auth.Session, destination string) error {
	return p.Users.UpstreamAuth(request, session, destination)
}

// Session reads identity for a native resource boundary; callers authorize access.
func (s *Service) Session(ctx context.Context, id string) (*apiv1.Session, error) {
	return s.store.GetSessionByID(ctx, id)
}

// ReadAuthority returns native ancestry and inherited revocation after the owning
// service authorizes access to the Session.
func (s *Service) ReadAuthority(ctx context.Context, id string) (*apiv1.GetSessionAuthorityResponse, error) {
	grant, active, err := s.store.GetSessionAuthority(ctx, id)
	if err != nil {
		return nil, err
	}
	return &apiv1.GetSessionAuthorityResponse{Authority: grant, Revoked: !active}, nil
}

// RevokeAuthority fences an independently authenticated initiator's subtree.
// The owning service additionally authorizes the Session mutation.
func (s *Service) RevokeAuthority(ctx context.Context, id string) error {
	principal, err := credentialOwner(ctx)
	if err != nil {
		return err
	}
	grant, _, err := s.store.GetSessionAuthority(ctx, id)
	if err != nil {
		return err
	}
	if grant.InitiatorId != principal {
		return fmt.Errorf("execution initiator mismatch")
	}
	return s.store.RevokeSessionAuthority(ctx, id)
}
