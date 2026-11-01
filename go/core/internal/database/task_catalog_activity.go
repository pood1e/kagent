package database

import (
	"context"

	"github.com/jackc/pgx/v5"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// ListCatalogAgentActivity aggregates published facts directly in PostgreSQL;
// it does not hydrate task histories or contact runtimes.
func (c *Client) ListCatalogAgentActivity(ctx context.Context, scope CatalogScope) ([]*apiv1.AgentActivity, error) {
	type row struct {
		Namespace           string
		AgentName           string
		ActiveTasks         uint32
		ToolCalls           uint64
		Working             bool
		PendingToolApproval bool
		Interrupted         bool
	}
	rows, err := queryMany(ctx, c.db, `
 SELECT namespace,agent_name,
 count(*) FILTER(WHERE state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED')) AS active_tasks,
 COALESCE(sum(tool_call_count),0)::bigint AS tool_calls,
 bool_or(state IN ('TASK_STATE_SUBMITTED','TASK_STATE_WORKING')) AS working,
 bool_or(pending_tool_approval AND state NOT IN ('TASK_STATE_COMPLETED','TASK_STATE_FAILED','TASK_STATE_CANCELED','TASK_STATE_REJECTED')) AS pending_tool_approval,
 bool_or(state IN ('TASK_STATE_INPUT_REQUIRED','TASK_STATE_AUTH_REQUIRED')) AS interrupted
 FROM catalog_tasks($1,$2,$3,$4,$5) GROUP BY namespace,agent_name ORDER BY namespace,agent_name
 `, pgx.RowToStructByName[row], scope.args()...)
	if err != nil {
		return nil, err
	}
	result := make([]*apiv1.AgentActivity, 0, len(rows))
	for _, row := range rows {
		result = append(result, &apiv1.AgentActivity{Agent: &apiv1.ResourceReference{Namespace: row.Namespace, Name: row.AgentName}, ActiveTasks: row.ActiveTasks, ToolCalls: row.ToolCalls, Working: row.Working, PendingToolApproval: row.PendingToolApproval, Interrupted: row.Interrupted})
	}
	return result, nil
}
