package session

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/runtimeauth"
)

type continuationContextKey struct{}

func dispatchOrigin(ctx context.Context, request *a2a.SendMessageRequest) *database.DispatchOrigin {
	if id, ok := ctx.Value(continuationContextKey{}).(uuid.UUID); ok {
		return &database.DispatchOrigin{ContinuationID: id}
	}
	if origin := runtimeauth.Origin(ctx); origin != nil {
		return &database.DispatchOrigin{SessionID: origin.SessionID, Background: request.Config != nil && request.Config.ReturnImmediately}
	}
	return nil
}
