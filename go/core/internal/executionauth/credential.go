package executionauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

func (s *Service) seal(principal string, token *oauth2.Token) ([]byte, error) {
	data, err := json.Marshal(token)
	if err != nil {
		return nil, fmt.Errorf("encode execution credential")
	}
	defer clear(data)
	return s.cipher.Seal(nil, nil, data, []byte(principal)), nil
}

func (s *Service) open(principal string, data []byte) (*oauth2.Token, error) {
	raw, err := s.cipher.Open(nil, nil, data, []byte(principal))
	if err != nil {
		return nil, fmt.Errorf("execution credential unavailable")
	}
	defer clear(raw)
	var token oauth2.Token
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, fmt.Errorf("execution credential unavailable")
	}
	return &token, nil
}

// authenticateToken receives credentials only from the authenticated request or
// configured HTTPS OAuth endpoint. Revalidation preserves the exact provider
// identity; errors omit OAuth payloads and credentials.
func (s *Service) authenticateToken(ctx context.Context, token *oauth2.Token, principalID string) (auth.Principal, error) {
	if !token.Valid() || token.Expiry.IsZero() {
		return auth.Principal{}, fmt.Errorf("execution credential expired or has no expiry")
	}
	caller, err := s.users.Authenticate(ctx, http.Header{"Authorization": {"Bearer " + token.AccessToken}}, nil)
	if err != nil || caller == nil || caller.Principal().ID != principalID || caller.Principal().RuntimeBinding != nil {
		return auth.Principal{}, fmt.Errorf("execution credential identity verification failed")
	}
	if expiry, ok := caller.(auth.ExpiringTokenSource); ok && !expiry.Expiry().IsZero() && expiry.Expiry().Before(token.Expiry) {
		token.Expiry = expiry.Expiry()
	}
	principal, err := s.Normalize(caller.Principal())
	if err != nil {
		return auth.Principal{}, fmt.Errorf("execution credential scope verification failed")
	}
	return principal, nil
}

func (s *Service) refresh(ctx context.Context, token *oauth2.Token, principal string) (*oauth2.Token, auth.Principal, error) {
	config, err := s.refreshConfig()
	if err != nil {
		return nil, auth.Principal{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.http)
	renewed, err := config.TokenSource(ctx, &oauth2.Token{RefreshToken: token.RefreshToken}).Token()
	if err != nil {
		return nil, auth.Principal{}, err
	}
	identity, err := s.authenticateToken(ctx, renewed, principal)
	return renewed, identity, err
}

func (s *Service) current(ctx context.Context, principal string) (*oauth2.Token, auth.Principal, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		row, err := s.store.GetDelegationCredential(ctx, principal)
		if err != nil {
			return nil, auth.Principal{}, fmt.Errorf("execution credential unavailable")
		}
		token, err := s.open(principal, row.Ciphertext)
		if err != nil {
			return nil, auth.Principal{}, err
		}
		if token.Valid() && (token.RefreshToken == "" || time.Until(token.Expiry) > 30*time.Second) {
			identity, err := s.authenticateToken(ctx, token, principal)
			return token, identity, err
		}
		if token.RefreshToken == "" {
			return nil, auth.Principal{}, fmt.Errorf("execution credential expired")
		}
		lease := uuid.New()
		claimed, err := s.store.ClaimDelegationCredential(ctx, principal, row.Version, lease)
		if err != nil {
			return nil, auth.Principal{}, fmt.Errorf("execution credential refresh unavailable")
		}
		if !claimed {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, auth.Principal{}, ctx.Err()
			case <-timer.C:
				continue
			}
		}
		renewed, identity, refreshErr := s.refresh(ctx, token, principal)
		if refreshErr != nil {
			var response *oauth2.RetrieveError
			invalid := errors.As(refreshErr, &response) && (response.ErrorCode == "invalid_grant" || response.ErrorCode == "invalid_client")
			// Identity changes invalidate this version. Transient failures release its
			// lease without discarding a credential that may recover on the next call.
			invalid = invalid || renewed != nil
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			var cleanupErr error
			if invalid {
				cleanupErr = s.store.InvalidateDelegationCredential(cleanup, principal, row.Version, lease)
			} else {
				_, cleanupErr = s.store.FinishDelegationCredential(cleanup, principal, row.Version, lease, row.Ciphertext)
			}
			stop()
			if cleanupErr != nil {
				return nil, auth.Principal{}, fmt.Errorf("execution credential refresh cleanup failed")
			}
			return nil, auth.Principal{}, fmt.Errorf("execution credential renewal failed")
		}
		ciphertext, err := s.seal(principal, renewed)
		if err != nil {
			return nil, auth.Principal{}, err
		}
		saved, err := s.store.FinishDelegationCredential(ctx, principal, row.Version, lease, ciphertext)
		if err != nil {
			return nil, auth.Principal{}, fmt.Errorf("persist renewed execution credential failed")
		}
		if !saved {
			continue
		}
		return renewed, identity, nil
	}
}

// capture preserves a registered renewable credential. A newer independently
// verified bearer rotates a nonrenewable source and extends its bounded grants.
func (s *Service) capture(ctx context.Context, caller auth.Session, create bool) (bool, time.Time, error) {
	principal := caller.Principal().ID
	for {
		row, err := s.store.GetDelegationCredential(ctx, principal)
		var version int64
		var existing *oauth2.Token
		if err == nil {
			existing, err = s.open(principal, row.Ciphertext)
			if err != nil {
				return false, time.Time{}, err
			}
			if existing.RefreshToken != "" {
				if !create {
					return true, time.Time{}, nil
				}
				if _, _, err := s.current(ctx, principal); err != nil {
					return false, time.Time{}, err
				}
				return true, time.Time{}, nil
			}
			version = row.Version
		} else if errors.Is(err, database.ErrNotFound) {
			if !create {
				return false, time.Time{}, nil
			}
		} else {
			return false, time.Time{}, fmt.Errorf("execution credential unavailable")
		}
		source, ok := caller.(auth.ExpiringTokenSource)
		if !ok || source.Expiry().IsZero() {
			return false, time.Time{}, fmt.Errorf("execution credential requires an expiry")
		}
		raw, err := source.Token(ctx)
		if err != nil || raw == "" {
			return false, time.Time{}, fmt.Errorf("execution bearer credential is required")
		}
		if existing != nil && existing.Valid() && !source.Expiry().After(existing.Expiry) {
			return false, existing.Expiry, nil
		}
		token := &oauth2.Token{AccessToken: raw, TokenType: "Bearer", Expiry: source.Expiry()}
		if _, err := s.authenticateToken(ctx, token, principal); err != nil {
			return false, time.Time{}, err
		}
		ciphertext, err := s.seal(principal, token)
		if err != nil {
			return false, time.Time{}, err
		}
		saved, err := s.store.CaptureDelegationCredential(ctx, principal, version, ciphertext, token.Expiry)
		if err != nil {
			return false, time.Time{}, fmt.Errorf("persist execution credential failed")
		}
		if saved {
			return false, token.Expiry, nil
		}
		if err := ctx.Err(); err != nil {
			return false, time.Time{}, err
		}
	}
}

// RegisterCredential binds a refreshed credential to the independent caller.
// Actors and delegated callers cannot replace a root user's source.
func (s *Service) RegisterCredential(ctx context.Context, refreshToken string) error {
	if s.config == nil || s.config.Oauth == nil {
		return fmt.Errorf("renewable execution credentials are not configured")
	}
	principal, err := credentialOwner(ctx)
	if err != nil {
		return err
	}
	if len(refreshToken) == 0 || len(refreshToken) > 16384 {
		return fmt.Errorf("invalid refresh credential")
	}
	token, _, err := s.refresh(ctx, &oauth2.Token{RefreshToken: refreshToken}, principal)
	if err != nil {
		return fmt.Errorf("delegation credential identity verification failed")
	}
	encrypted, err := s.seal(principal, token)
	if err != nil {
		return err
	}
	return s.store.SaveDelegationCredential(ctx, principal, encrypted)
}

func credentialOwner(ctx context.Context) (string, error) {
	caller, ok := auth.AuthSessionFrom(ctx)
	if _, shared := auth.ShareContextFrom(ctx); shared || !ok || caller.Principal().ID == "" || caller.Principal().RuntimeBinding != nil || caller.Principal().Execution != nil {
		return "", fmt.Errorf("independent principal authentication is required")
	}
	return caller.Principal().ID, nil
}

// RevokeCredential removes the caller's durable source. Subsequent resolutions
// fail closed for continuations, retries and gateway permission checks.
func (s *Service) RevokeCredential(ctx context.Context) error {
	principal, err := credentialOwner(ctx)
	if err != nil {
		return err
	}
	return s.store.DeleteDelegationCredential(ctx, principal)
}
