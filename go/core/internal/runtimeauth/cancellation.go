package runtimeauth

import (
	"context"

	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type cancellationKey struct{}
type cancellation struct{ SessionID, TaskID string }

// CancellationContext confines a durable controller cleanup attempt to one
// persisted task. Its context is never constructed from public request metadata.
func CancellationContext(ctx context.Context, sessionID, taskID string) context.Context {
	ctx = auth.AuthSessionTo(ctx, auth.ControlPlaneSession{})
	return context.WithValue(ctx, cancellationKey{}, cancellation{SessionID: sessionID, TaskID: taskID})
}

func cancellationFrom(ctx context.Context) (cancellation, bool) {
	session, _ := auth.AuthSessionFrom(ctx)
	if _, internal := session.(auth.ControlPlaneSession); !internal {
		return cancellation{}, false
	}
	scope, ok := ctx.Value(cancellationKey{}).(cancellation)
	return scope, ok
}
