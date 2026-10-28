package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Start creates an isolated disposable database on a supplied test server.
func Start(ctx context.Context, baseURL string) (string, func(), error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", nil, err
	}
	db, err := sql.Open("pgx", baseURL)
	if err != nil {
		return "", nil, err
	}
	defer db.Close()
	name := "kagent_test_" + uuid.NewString()
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := db.ExecContext(ctx, "CREATE DATABASE "+identifier); err != nil {
		return "", nil, err
	}
	parsed.Path = "/" + name
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		admin, err := sql.Open("pgx", baseURL)
		if err != nil {
			fmt.Printf("open external test server for cleanup: %v\n", err)
			return
		}
		defer admin.Close()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			fmt.Printf("drop external test database: %v\n", err)
		}
	}
	return parsed.String(), cleanup, nil
}
