package database

import (
	"context"
	"time"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// CatalogSessionPage contains activity summaries selected before Session decoding.
type CatalogSessionPage struct {
	Sessions []*apiv1.SessionActivity
	Total    int
	NextID   string
	NextTime *time.Time
}

// ListCatalogSessionActivity aggregates published descendant work and searches
// indexed names/prompts before paging. Empty Sessions retain their native identity.
func (c *Client) ListCatalogSessionActivity(ctx context.Context, q CatalogTaskQuery) (CatalogSessionPage, error) {
	result := CatalogSessionPage{Sessions: []*apiv1.SessionActivity{}}
	agent := q.Agent.GetNamespace() + "/" + q.Agent.GetName()
	args := append(q.Scope.args(), agent, catalogPattern(q.Query))
	err := catalogRead(ctx, c, q.Scope, func(tx pgx.Tx) error {
		total, err := queryOne(ctx, tx, `
 SELECT count(*) FROM catalog_session_activity($1,$2,$3,$4,$5,$6,$7)
 `, pgx.RowTo[int], args...)
		if err != nil {
			return err
		}
		type row struct {
			SessionID           string
			State               *string
			PendingToolApproval bool
			UpdatedAt           *time.Time
			Creator             string
			RuntimeState        string
			Operation           string
			PreparedRevision    string
			SessionData         []byte
		}
		rows, err := queryMany(ctx, tx, `
 SELECT p.session_id::text,p.state,p.pending_tool_approval,p.updated_at,
 s.creator,s.runtime_state,s.operation,s.prepared_revision,s.session_data
 FROM catalog_session_activity($1,$2,$3,$4,$5,$6,$7) p
 JOIN task_catalog_session s ON s.session_id=p.session_id
 WHERE ($8::timestamptz IS NULL OR COALESCE(p.updated_at,'epoch'::timestamptz)<$8 OR (COALESCE(p.updated_at,'epoch'::timestamptz)=$8 AND p.session_id::text>$9))
 ORDER BY COALESCE(p.updated_at,'epoch'::timestamptz) DESC,p.session_id LIMIT $10
 `, pgx.RowToStructByName[row], append(args, q.AfterTime, q.AfterID, q.Limit+1)...)
		if err != nil {
			return err
		}
		more := len(rows) > q.Limit
		if more {
			rows = rows[:q.Limit]
		}
		for _, row := range rows {
			session, err := catalogSession(catalogTaskRow{SessionID: row.SessionID, Creator: row.Creator, RuntimeState: row.RuntimeState, Operation: row.Operation, PreparedRevision: row.PreparedRevision, SessionData: row.SessionData})
			if err != nil {
				return err
			}
			activity := &apiv1.TaskActivity{PendingToolApproval: row.PendingToolApproval}
			if row.UpdatedAt != nil {
				activity.UpdatedAt = timestamppb.New(*row.UpdatedAt)
			}
			if row.State != nil {
				state, ok := a2apb.TaskState_value[*row.State]
				if !ok {
					return invalidCatalogState(*row.State)
				}
				value := a2apb.TaskState(state)
				activity.State = &value
			}
			result.Sessions = append(result.Sessions, &apiv1.SessionActivity{Session: session, Activity: activity})
		}
		result.Total = total
		if more {
			result.NextID = rows[len(rows)-1].SessionID
			result.NextTime = rows[len(rows)-1].UpdatedAt
		}
		return nil
	})
	return result, err
}
