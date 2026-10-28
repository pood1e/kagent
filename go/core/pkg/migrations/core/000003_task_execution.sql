-- +goose Up
CREATE TABLE task_execution (
    task_id TEXT PRIMARY KEY REFERENCES session_task(id) ON DELETE CASCADE,
    history_id UUID NOT NULL,
    parent_task_id TEXT REFERENCES task_execution(task_id) ON DELETE RESTRICT,
    root_task_id TEXT NOT NULL REFERENCES task_execution(task_id) ON DELETE RESTRICT,
    relation TEXT NOT NULL CHECK (relation IN ('TASK_RELATION_ROOT', 'TASK_RELATION_DELEGATION', 'TASK_RELATION_CONTINUATION')),
    background BOOLEAN NOT NULL DEFAULT FALSE,
    FOREIGN KEY (history_id, task_id) REFERENCES session_task(history_id, id) ON DELETE CASCADE,
    CHECK ((parent_task_id IS NULL) = (relation = 'TASK_RELATION_ROOT')),
    CHECK (relation <> 'TASK_RELATION_ROOT' OR (root_task_id = task_id AND NOT background)),
    CHECK (NOT background OR relation = 'TASK_RELATION_DELEGATION'),
    CHECK (parent_task_id IS NULL OR parent_task_id <> task_id)
);
CREATE INDEX task_execution_tree_idx ON task_execution (root_task_id, task_id);
CREATE INDEX task_execution_parent_idx ON task_execution (parent_task_id, task_id);

-- Existing tasks start independent roots; new relationships are admitted natively.
INSERT INTO task_execution (task_id, history_id, root_task_id, relation)
SELECT id, history_id, id, 'TASK_RELATION_ROOT' FROM session_task;

CREATE TABLE task_dispatch (
    session_id UUID PRIMARY KEY REFERENCES session(id) ON DELETE CASCADE,
    dispatch_id UUID NOT NULL UNIQUE,
    parent_task_id TEXT REFERENCES task_execution(task_id) ON DELETE RESTRICT,
    root_task_id TEXT REFERENCES task_execution(task_id) ON DELETE RESTRICT,
    relation TEXT NOT NULL CHECK (relation IN ('TASK_RELATION_ROOT', 'TASK_RELATION_DELEGATION', 'TASK_RELATION_CONTINUATION')),
    background BOOLEAN NOT NULL DEFAULT FALSE,
    CHECK ((parent_task_id IS NULL) = (relation = 'TASK_RELATION_ROOT')),
    CHECK ((root_task_id IS NULL) = (relation = 'TASK_RELATION_ROOT')),
    CHECK (NOT background OR relation = 'TASK_RELATION_DELEGATION')
);

CREATE TABLE task_cancellation (
    task_id TEXT PRIMARY KEY REFERENCES task_execution(task_id) ON DELETE CASCADE,
    completed BOOLEAN NOT NULL DEFAULT FALSE,
    executor_id UUID,
    executor_expires_at TIMESTAMPTZ,
    CHECK ((executor_id IS NULL) = (executor_expires_at IS NULL))
);
CREATE INDEX task_cancellation_pending_idx ON task_cancellation (task_id) WHERE NOT completed;

-- +goose Down
DROP TABLE task_cancellation;
DROP TABLE task_dispatch;
DROP TABLE task_execution;
