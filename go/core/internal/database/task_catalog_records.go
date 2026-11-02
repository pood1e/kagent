package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// ListCatalogTaskRecords filters and pages published metadata before hydrating the
// selected page. Totals and payloads share one read-only repeatable-read snapshot.
func (c *Client) ListCatalogTaskRecords(ctx context.Context, q CatalogTaskQuery) (CatalogRecords, error) {
	result := CatalogRecords{}
	filters, err := catalogFilters(q.Filters)
	if err != nil {
		return result, err
	}
	agent := ""
	if q.Agent != nil {
		agent = q.Agent.Namespace + "/" + q.Agent.Name
	}
	args := append(q.Scope.args(), agent, filters)
	err = catalogRead(ctx, c, q.Scope, func(tx pgx.Tx) error {
		total, err := queryOne(ctx, tx, `
 SELECT count(*) FROM catalog_record_candidates($1,$2,$3,$4,$5,$6,$7) t
 `, pgx.RowTo[int], args...)
		if err != nil {
			return err
		}
		rows, err := queryMany(ctx, tx, `
 SELECT task_id AS id,COALESCE(status_timestamp,created_at) AS time FROM catalog_record_candidates($1,$2,$3,$4,$5,$6,$7) t
 WHERE ($8::timestamptz IS NULL OR COALESCE(status_timestamp,created_at)<$8 OR (COALESCE(status_timestamp,created_at)=$8 AND task_id>$9))
 ORDER BY COALESCE(status_timestamp,created_at) DESC,task_id LIMIT $10
 `, pgx.RowToStructByName[catalogPosition], append(args, q.AfterTime, q.AfterID, q.Limit+1)...)
		if err != nil {
			return err
		}
		more := len(rows) > q.Limit
		if more {
			rows = rows[:q.Limit]
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		result, err = readCatalogPayloads(ctx, tx, q.Scope, ids, q.View)
		if err != nil {
			return err
		}
		result.Total = total
		if more {
			result.NextID = rows[len(rows)-1].ID
			result.NextTime = rows[len(rows)-1].Time
		}
		if len(ids) > 0 {
			if err := setCatalogBackground(ctx, tx, q.Scope, result.Tasks, q.BoundaryAgents); err != nil {
				return err
			}
		}
		if q.AfterID == "" && q.Scope.AnchorSessionID != "" {
			return appendCatalogAnchor(ctx, tx, q.Scope, &result)
		}
		return nil
	})
	return result, err
}

func setCatalogBackground(ctx context.Context, db dbExecutor, scope CatalogScope, tasks []*apiv1.CatalogTask, boundaries []string) error {
	ids := make([]string, len(tasks))
	for i, task := range tasks {
		ids[i] = task.Task.Id
	}
	if boundaries == nil {
		boundaries = []string{}
	}
	type fact struct {
		ID         string
		Background bool
	}
	rows, err := queryMany(ctx, db, `
 WITH RECURSIVE chain AS(
 SELECT task_id AS id,task_id,parent_task_id,COALESCE(background,FALSE) AS background,ARRAY[task_id] AS path
 FROM catalog_tasks($1,$2,$3,$4,$5) WHERE task_id=ANY($6::text[])
 UNION ALL
 SELECT c.id,p.task_id,p.parent_task_id,c.background OR COALESCE(p.background,FALSE),c.path||p.task_id
 FROM chain c JOIN LATERAL (
 SELECT task_id,parent_task_id,background,namespace||'/'||agent_name AS agent
 FROM catalog_tasks($1,$2,$3,$4,$5) WHERE task_id=c.parent_task_id LIMIT 1
 ) p ON TRUE WHERE NOT p.agent=ANY($7::text[]) AND NOT p.task_id=ANY(c.path))
 SELECT id,bool_or(background) AS background FROM chain GROUP BY id
 `, pgx.RowToStructByName[fact], append(scope.args(), ids, boundaries)...)
	if err != nil {
		return err
	}
	values := map[string]bool{}
	for _, row := range rows {
		values[row.ID] = row.Background
	}
	for _, task := range tasks {
		task.Background = values[task.Task.Id]
	}
	return nil
}

func appendCatalogAnchor(ctx context.Context, db dbExecutor, scope CatalogScope, result *CatalogRecords) error {
	for _, session := range result.Sessions {
		if session.Id == scope.AnchorSessionID {
			return nil
		}
	}
	row, err := queryOne(ctx, db, `
 SELECT session_id::text,creator,runtime_state,operation,prepared_revision,session_data FROM catalog_sessions($1,$2,$3,$4,$5) WHERE session_id=$6::uuid
 `, pgx.RowToStructByNameLax[catalogTaskRow], append(scope.args(), scope.AnchorSessionID)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	session, err := catalogSession(row)
	if err == nil {
		result.Sessions = append(result.Sessions, session)
	}
	return err
}
