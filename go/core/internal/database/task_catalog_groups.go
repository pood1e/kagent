package database

import (
	"context"
	"strings"
	"time"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// CatalogGroups contains the page's authorized anchors and deduplicated members.
type CatalogGroups struct {
	CatalogRecords
	Groups []*apiv1.TaskGroup
}

type catalogGroupRow struct {
	AnchorID            string
	AnchorSessionID     string
	InitiatorID         *string
	InitiatorSessionID  *string
	State               string
	PendingToolApproval bool
	UpdatedAt           *time.Time
	MemberIDs           []string
}

func catalogPattern(query string) string {
	if query == "" {
		return ""
	}
	return "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ToLower(query)) + "%"
}

// ListCatalogTaskGroups applies trusted forest grouping and activity predicates
// before pagination. The page's bodies are loaded only after selecting anchors.
func (c *Client) ListCatalogTaskGroups(ctx context.Context, q CatalogTaskQuery) (CatalogGroups, error) {
	result := CatalogGroups{Groups: []*apiv1.TaskGroup{}}
	filters, err := catalogFilters(q.Filters)
	if err != nil {
		return result, err
	}
	boundaries := q.BoundaryAgents
	if boundaries == nil {
		boundaries = []string{}
	}
	agent := ""
	if q.Agent != nil {
		agent = q.Agent.Namespace + "/" + q.Agent.Name
	}
	args := append(q.Scope.args(), boundaries, agent, catalogPattern(q.Query), filters)
	err = catalogRead(ctx, c, q.Scope, func(tx pgx.Tx) error {
		total, err := queryOne(ctx, tx, `
 SELECT count(*) FROM catalog_task_groups($1,$2,$3,$4,$5,$6,$7,$8,$9)
 `, pgx.RowTo[int], args...)
		if err != nil {
			return err
		}
		rows, err := queryMany(ctx, tx, `
 SELECT anchor_id,anchor_session_id::text,initiator_id,initiator_session_id::text,state,pending_tool_approval,updated_at,member_ids
 FROM catalog_task_groups($1,$2,$3,$4,$5,$6,$7,$8,$9)
 WHERE ($10::timestamptz IS NULL OR COALESCE(updated_at,'epoch'::timestamptz)<$10
 OR (COALESCE(updated_at,'epoch'::timestamptz)=$10 AND anchor_id>$11))
 ORDER BY COALESCE(updated_at,'epoch'::timestamptz) DESC,anchor_id LIMIT $12
 `, pgx.RowToStructByName[catalogGroupRow], append(args, q.AfterTime, q.AfterID, q.Limit+1)...)
		if err != nil {
			return err
		}
		more := len(rows) > q.Limit
		if more {
			rows = rows[:q.Limit]
		}
		ids := []string{}
		seen := map[string]bool{}
		for _, row := range rows {
			for _, id := range row.MemberIDs {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		result.CatalogRecords, err = readCatalogPayloads(ctx, tx, q.Scope, ids, q.View)
		if err != nil {
			return err
		}
		result.Total = total
		for _, row := range rows {
			value, ok := a2apb.TaskState_value[row.State]
			if !ok {
				return invalidCatalogState(row.State)
			}
			state := a2apb.TaskState(value)
			activity := &apiv1.TaskActivity{State: &state, PendingToolApproval: row.PendingToolApproval}
			if row.UpdatedAt != nil {
				activity.UpdatedAt = timestamppb.New(*row.UpdatedAt)
			}
			group := &apiv1.TaskGroup{Root: &apiv1.TaskReference{SessionId: row.AnchorSessionID, TaskId: row.AnchorID}, Activity: activity}
			if row.InitiatorID != nil {
				group.Initiator = &apiv1.TaskReference{SessionId: *row.InitiatorSessionID, TaskId: *row.InitiatorID}
			}
			result.Groups = append(result.Groups, group)
		}
		if more {
			result.NextID = rows[len(rows)-1].AnchorID
			result.NextTime = rows[len(rows)-1].UpdatedAt
		}
		return nil
	})
	return result, err
}
