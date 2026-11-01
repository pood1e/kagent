package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// CatalogScope is an authorized collection boundary. Nil SessionIDs is unrestricted;
// an empty non-nil slice permits no sessions. Callers derive it from current policy.
type CatalogScope struct {
	Namespace        string
	OwnerID          string
	OtherInitiatorID string
	RootSessionID    string
	AnchorSessionID  string
	SessionIDs       []string
}

func (s CatalogScope) args() []any {
	var root *string
	if s.RootSessionID != "" {
		root = &s.RootSessionID
	}
	return []any{s.Namespace, s.OwnerID, s.OtherInitiatorID, root, s.SessionIDs}
}

// CatalogFilter combines a native state with an optional approval predicate.
type CatalogFilter struct {
	State           *string `json:"state"`
	PendingApproval *bool   `json:"pending_tool_approval"`
}

// CatalogTaskQuery selects one deterministically ordered, authorized page.
type CatalogTaskQuery struct {
	Scope          CatalogScope
	Agent          *apiv1.ResourceReference
	Filters        []CatalogFilter
	BoundaryAgents []string
	Limit          int
	AfterTime      *time.Time
	AfterID        string
	View           apiv1.TaskCatalogView
	Query          string
}

// CatalogRecords contains only selected records and the sessions that own them.
type CatalogRecords struct {
	Tasks    []*apiv1.CatalogTask
	Sessions []*apiv1.Session
	Total    int
	NextID   string
	NextTime *time.Time
}

type catalogPosition struct {
	ID   string
	Time *time.Time
}

func catalogFilters(filters []CatalogFilter) (string, error) {
	if filters == nil {
		filters = []CatalogFilter{}
	}
	data, err := json.Marshal(filters)
	return string(data), err
}

func catalogRead(ctx context.Context, client *Client, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, client.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}

// CatalogHasRenewableSession observes whether this scope contains caller-initiated
// renewable sessions. The service renews credentials once outside the transaction.
func (c *Client) CatalogHasRenewableSession(ctx context.Context, scope CatalogScope, caller string) (bool, error) {
	return queryOne(ctx, c.db, `
 SELECT EXISTS(SELECT 1 FROM catalog_sessions($1,$2,$3,$4,$5)
 WHERE initiator_id=$6 AND expires_at IS NOT NULL)
 `, pgx.RowTo[bool], append(scope.args(), caller)...)
}

func invalidCatalogState(state string) error {
	return fmt.Errorf("unknown catalog task state %q", state)
}
