package runtimeauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

// originSession retains the authenticated caller's authority and separately
// records the verified runtime that originated a delegated MCP call.
type originSession struct {
	auth.Session
	origin runtimeSession
}

// Origin identifies the verified initiating runtime, never a caller supplied ID.
func Origin(ctx context.Context) *auth.RuntimeBinding {
	session, _ := auth.AuthSessionFrom(ctx)
	if delegated, ok := session.(originSession); ok {
		return delegated.origin.Principal().RuntimeBinding
	}
	if session != nil {
		return session.Principal().RuntimeBinding
	}
	return nil
}

// withOrigin binds an actor proof to the same owner as the authenticated caller.
func (p *Provider) withOrigin(ctx context.Context, headers http.Header, caller auth.Session) (auth.Session, error) {
	values := headers.Values("X-Substrate-Actor-Token")
	if len(values) == 0 {
		return caller, nil
	}
	if len(values) != 1 || caller == nil || caller.Principal().Execution != nil {
		return nil, fmt.Errorf("one originating actor proof is required")
	}
	proof := strings.TrimPrefix(values[0], "Bearer ")
	origin, err := p.actors.Authenticate(ctx, http.Header{"Authorization": {"Bearer " + proof}}, nil)
	if err != nil {
		return nil, err
	}
	session, err := Session(auth.AuthSessionTo(ctx, origin), p.store)
	if err != nil {
		return nil, err
	}
	if session.Creator == "" || session.Creator != caller.Principal().ID {
		return nil, fmt.Errorf("originating actor belongs to another caller")
	}
	return originSession{Session: caller, origin: runtimeSession{sessionID: session.Id, atespace: origin.(runtimeSession).atespace, actorUID: origin.(runtimeSession).actorUID, owner: session.Creator}}, nil
}

func (s originSession) Principal() auth.Principal {
	principal := s.Session.Principal()
	principal.RuntimeBinding = s.origin.Principal().RuntimeBinding
	return principal
}

func authSessionOrigin(ctx context.Context) (auth.Principal, bool) {
	session, _ := auth.AuthSessionFrom(ctx)
	origin, ok := session.(originSession)
	if !ok {
		return auth.Principal{}, false
	}
	return origin.Session.Principal(), true
}
