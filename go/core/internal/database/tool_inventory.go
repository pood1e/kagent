package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// SaveToolInventory publishes the last authenticated SDK observation for a
// retained revision. Revision deletion atomically removes its observations.
func (c *Client) SaveToolInventory(ctx context.Context, catalog *apiv1.BuiltinToolCatalog) error {
	data, err := proto.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode tool inventory: %w", err)
	}
	return execSQL(ctx, c.db, `
	 INSERT INTO runtime_tool_inventory(revision, data) VALUES ($1, $2)
	 ON CONFLICT (revision) DO UPDATE SET data = EXCLUDED.data
	`, catalog.GetRevision(), data)
}

// GetToolInventory returns the last observation. Missing revisions and
// unobserved revisions return ErrNotFound; malformed payloads return errors.
func (c *Client) GetToolInventory(ctx context.Context, revision string) (*apiv1.BuiltinToolCatalog, error) {
	data, err := queryOne(ctx, c.db, `SELECT data FROM runtime_tool_inventory WHERE revision = $1`, pgx.RowTo[[]byte], revision)
	if err != nil {
		return nil, notFoundOr(err)
	}
	catalog := &apiv1.BuiltinToolCatalog{}
	if err := proto.Unmarshal(data, catalog); err != nil {
		return nil, fmt.Errorf("decode tool inventory: %w", err)
	}
	if catalog.Revision != revision || catalog.ObservedAt == nil {
		return nil, fmt.Errorf("invalid stored tool inventory")
	}
	return catalog, nil
}
