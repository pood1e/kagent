-- +goose Up
ALTER TABLE task_dispatch ADD COLUMN resumed_task_id TEXT REFERENCES task_execution(task_id);
ALTER TABLE task_execution ADD COLUMN resumed_task_id TEXT REFERENCES task_execution(task_id);

-- +goose Down
ALTER TABLE task_execution DROP COLUMN resumed_task_id;
ALTER TABLE task_dispatch DROP COLUMN resumed_task_id;
