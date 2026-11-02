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
	WorkspaceUID     string
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

func catalogRead(ctx context.Context, client *Client, scope CatalogScope, fn func(pgx.Tx) error) error {
	if scope.WorkspaceUID == "" {
		return fmt.Errorf("catalog workspace identity is required")
	}
	return pgx.BeginTxFunc(ctx, client.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('kagent.catalog_workspace_uid', $1, TRUE)`, scope.WorkspaceUID); err != nil {
			return fmt.Errorf("bind catalog workspace identity: %w", err)
		}
		// Authorized forests range from empty to many thousands of anchors. Bind
		// cardinalities when planning nested SQL functions as well as the page.
		if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = force_custom_plan`); err != nil {
			return fmt.Errorf("configure catalog query planning: %w", err)
		}
		return fn(tx)
	})
}

// CatalogHasRenewableSession observes whether this scope contains caller-initiated
// renewable sessions. The service renews credentials once outside the transaction.
func (c *Client) CatalogHasRenewableSession(ctx context.Context, scope CatalogScope, caller string) (bool, error) {
	var renewable bool
	err := catalogRead(ctx, c, scope, func(tx pgx.Tx) error {
		var err error
		renewable, err = queryOne(ctx, tx, `
 SELECT EXISTS(SELECT 1 FROM catalog_sessions($1,$2,$3,$4,$5)
 WHERE initiator_id=$6 AND expires_at IS NOT NULL)
 `, pgx.RowTo[bool], append(scope.args(), caller)...)
		return err
	})
	return renewable, err
}

func invalidCatalogState(state string) error {
	return fmt.Errorf("unknown catalog task state %q", state)
}
