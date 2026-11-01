// Package taskcatalog serves authorization-aware pages from published task indexes.
package taskcatalog

import (
	"context"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
)

type Store interface {
	CatalogHasRenewableSession(context.Context, database.CatalogScope, string) (bool, error)
	ListCatalogTaskGroups(context.Context, database.CatalogTaskQuery) (database.CatalogGroups, error)
	ListCatalogTaskRecords(context.Context, database.CatalogTaskQuery) (database.CatalogRecords, error)
	ListCatalogSessionActivity(context.Context, database.CatalogTaskQuery) (database.CatalogSessionPage, error)
	ListCatalogAgentActivity(context.Context, database.CatalogScope) ([]*apiv1.AgentActivity, error)
}

var _ Store = (*database.Client)(nil)

type Sessions interface {
	CatalogReadScope(context.Context, *apiv1.TaskCatalogScope) (database.CatalogScope, error)
	RenewCatalogRead(context.Context) error
}

type Service struct {
	store    Store
	sessions Sessions
}

func NewService(store Store, sessions Sessions) *Service {
	return &Service{store: store, sessions: sessions}
}

type catalogInput struct {
	kind       string
	message    proto.Message
	scope      *apiv1.TaskCatalogScope
	page       *apiv1.PageRequest
	agent      *apiv1.ResourceReference
	activities []*apiv1.TaskActivityFilter
	boundaries []*apiv1.ResourceReference
	view       apiv1.TaskCatalogView
	query      string
}

func (s *Service) prepare(ctx context.Context, input catalogInput) (database.CatalogTaskQuery, string, error) {
	q := database.CatalogTaskQuery{}
	if err := protovalidate.Validate(input.message); err != nil {
		return q, "", serviceerrors.NewInvalidArgument("Invalid task catalog request", err)
	}
	scope, err := s.sessions.CatalogReadScope(ctx, input.scope)
	if err != nil {
		return q, "", err
	}
	q.Scope = scope
	q.Agent = input.agent
	q.View = input.view
	q.Query = strings.TrimSpace(input.query)
	q.Limit = int(input.page.GetLimit())
	if q.Limit == 0 {
		q.Limit = 50
	}
	q.BoundaryAgents = boundaryKeys(input.boundaries)
	for _, filter := range input.activities {
		item := database.CatalogFilter{PendingApproval: filter.PendingToolApproval}
		if filter.State != nil {
			state := filter.State.String()
			item.State = &state
		}
		q.Filters = append(q.Filters, item)
	}
	binding, err := catalogBinding(input.kind, q)
	if err != nil {
		return q, "", serviceerrors.NewInternal("Failed to bind catalog page", err)
	}
	q.AfterTime, q.AfterID, err = readCursor(input.page.GetPageToken(), binding)
	if err != nil {
		return q, "", serviceerrors.NewInvalidArgument("Invalid catalog page token", err)
	}
	caller, ok := auth.AuthSessionFrom(ctx)
	if ok && scope.OwnerID != "" && scope.AnchorSessionID == "" {
		principal := caller.Principal()
		_, shared := auth.ShareContextFrom(ctx)
		if !shared && principal.ID != "" && principal.RuntimeBinding == nil && principal.Execution == nil {
			needed, err := s.store.CatalogHasRenewableSession(ctx, scope, principal.ID)
			if err != nil {
				return q, "", serviceerrors.NewInternal("Failed to inspect catalog authority", err)
			}
			if needed {
				if err := s.sessions.RenewCatalogRead(ctx); err != nil {
					return q, "", err
				}
			}
		}
	}
	return q, binding, nil
}
