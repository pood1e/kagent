-- +goose Up
ALTER TABLE scheduled_run ADD COLUMN authority BYTEA;
ALTER TABLE scheduled_run_execution ADD COLUMN authority BYTEA;

-- +goose Down
ALTER TABLE scheduled_run_execution DROP COLUMN authority;
ALTER TABLE scheduled_run DROP COLUMN authority;
