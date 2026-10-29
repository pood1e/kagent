package executionauth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

const sessionCredentialPrefix = "kagent-session."

var sessionCredentialPurpose = []byte("kagent/session-invocation/v1")

// CredentialStatus describes the independently authenticated caller's source.
func (s *Service) CredentialStatus(ctx context.Context) (bool, error) {
	id, err := credentialOwner(ctx)
	if err != nil {
		return false, err
	}
	row, err := s.store.GetDelegationCredential(ctx, id)
	if errors.Is(err, database.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if s.cipher == nil {
		return false, nil
	}
	token, err := s.open(id, row.Ciphertext)
	if err != nil || token.RefreshToken == "" {
		return false, err
	}
	_, _, err = s.current(ctx, id)
	return err == nil, err
}

// IssueSessionCredential grants a bounded, single-conversation capability. The
// opaque credential contains native authority, never an initiator bearer token.
func (s *Service) IssueSessionCredential(ctx context.Context, request *apiv1.IssueSessionCredentialRequest) (*apiv1.IssueSessionCredentialResponse, error) {
	if request == nil || s.cipher == nil {
		return nil, fmt.Errorf("session credentials are unavailable")
	}
	if err := protovalidate.Validate(request); err != nil {
		return nil, err
	}
	id, err := credentialOwner(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := s.Resolve(ctx, request.SessionId)
	if err != nil || resolved.Principal().ID != id {
		return nil, fmt.Errorf("session credential initiator mismatch")
	}
	caller, _ := auth.AuthSessionFrom(ctx)
	principal, err := s.Normalize(caller.Principal())
	if err != nil {
		return nil, err
	}
	for _, scope := range resolved.Principal().Scopes {
		if !slices.Contains(principal.Scopes, scope) {
			return nil, fmt.Errorf("session credential exceeds caller authority")
		}
	}
	grant := proto.CloneOf(resolved.Principal().Execution)
	grant.ScopeCeiling = &apiv1.ScopeCeiling{Scopes: slices.Clone(resolved.Principal().Scopes)}
	expires := time.Now().Add(request.Ttl.AsDuration())
	if grant.ExpiresAt != nil && grant.ExpiresAt.AsTime().Before(expires) {
		expires = grant.ExpiresAt.AsTime()
	}
	grant.ExpiresAt = timestamppb.New(expires)
	payload, err := proto.Marshal(grant)
	if err != nil {
		return nil, fmt.Errorf("encode session credential")
	}
	defer clear(payload)
	sealed := s.cipher.Seal(nil, nil, payload, sessionCredentialPurpose)
	return &apiv1.IssueSessionCredentialResponse{Credential: sessionCredentialPrefix + base64.RawURLEncoding.EncodeToString(sealed), ExpiresAt: grant.ExpiresAt}, nil
}

func sessionCredential(headers http.Header) (string, bool) {
	for _, authorization := range headers.Values("Authorization") {
		scheme, raw, ok := strings.Cut(authorization, " ")
		if ok && strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(raw, sessionCredentialPrefix) {
			if len(headers.Values("Authorization")) != 1 {
				return "", true
			}
			return strings.TrimPrefix(raw, sessionCredentialPrefix), true
		}
	}
	return "", false
}

func (s *Service) authenticateSessionCredential(ctx context.Context, raw string) (auth.Session, error) {
	denied := fmt.Errorf("session credential is invalid or unavailable")
	if s.cipher == nil || len(raw) > 16384 {
		return nil, denied
	}
	sealed, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, denied
	}
	payload, err := s.cipher.Open(nil, nil, sealed, sessionCredentialPurpose)
	if err != nil {
		return nil, denied
	}
	defer clear(payload)
	grant := &apiv1.SessionAuthority{}
	if err := proto.Unmarshal(payload, grant); err != nil || grant.ScopeCeiling == nil || grant.ExpiresAt == nil || grant.ExpiresAt.CheckValid() != nil || !time.Now().Before(grant.ExpiresAt.AsTime()) {
		return nil, denied
	}
	resolved, err := s.Resolve(ctx, grant.SessionId)
	if err != nil || resolved.Principal().ID != grant.InitiatorId || resolved.Principal().Execution.RootSessionId != grant.RootSessionId {
		return nil, denied
	}
	principal := resolved.Principal()
	principal.Scopes = intersect(principal.Scopes, grant.ScopeCeiling.Scopes)
	principal.Claims[s.config.ScopeClaim] = slices.Clone(principal.Scopes)
	source, _ := resolved.(auth.TokenSource)
	return auth.NewCredentialSession(principal, boundedSource{source: source, expires: grant.ExpiresAt.AsTime()}), nil
}

type boundedSource struct {
	source  auth.TokenSource
	expires time.Time
}

func (s boundedSource) Token(ctx context.Context) (string, error) {
	if !time.Now().Before(s.expires) {
		return "", fmt.Errorf("session credential expired")
	}
	return s.source.Token(ctx)
}
func (s boundedSource) Expiry() time.Time { return s.expires }
