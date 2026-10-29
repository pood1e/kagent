-- +goose Up
ALTER TABLE task_continuation
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp();

-- +goose Down
ALTER TABLE task_continuation
    DROP COLUMN next_attempt_at,
    DROP COLUMN attempt_count;
