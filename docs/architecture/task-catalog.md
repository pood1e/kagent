# Published task catalog

`TaskCatalogService` provides authorized collections of published A2A tasks:
operation groups, task records, conversation activity, and Agent activity.
The contract reuses upstream A2A tasks, native Sessions, and immutable
`TaskExecution` references.

## Read path

Session authorization resolves the current namespace UID, ownership or live-tree
boundary, and permitted Session IDs. A read-only transaction binds that UID to
the catalog SQL functions; PostgreSQL intersects it with the Session's captured
`workspace_uid` before activity aggregation, literal-substring search,
deterministic ordering, and keyset paging.
Counts and page contents share a read-only repeatable-read transaction.
Page tokens bind the current authorized scope, workspace UID, and selection.
The transaction selects PostgreSQL custom plans for parameter-sensitive scopes
and nested tree functions; both planning and workspace settings expire with the transaction.

Group anchors use the requested boundary Agents. Descendant traversal retains
the complete authorized execution tree, including nested groups and background
work. Foreground activity follows native interaction and continuation semantics.
Active-only group queries start from the partial active-task index and follow
authorized ancestors; tree aggregation selects the relevant native execution
roots. Historical exact counts aggregate their authorized metadata range.

Selected task bodies and published message history are loaded after paging.
Summary views carry indexed identity, prompt, status, and creation time;
detail views include canonical payloads. Agent activity aggregates indexed facts.

## Publication and authority

The administrator's PostgreSQL setup installs shared `pg_trgm` search support.
Schema-owner migrations reuse its installed operator classes across core schemas.

TaskStore updates prompt, pending interaction, and tool-call indexes in the
transaction that publishes task state and history. Ordinary narration reuses
retained history indexes. Session creation, naming, lifecycle, and fork operations
update their corresponding indexed fields through the owning store transaction.

A live Session authorizes its immutable authority tree. A deleted ancestor
remains a lineage reference while visible contents come from live authorized
Sessions. Shares and bound execution credentials select their exact Session.
Unbound human collection reads renew eligible delegation once per request.

## Code navigation

- `proto/kagent/api/v1/task_catalog.proto`: collection contracts and validation.
- `go/core/internal/service/taskcatalog`: request orchestration and page binding.
- `go/core/internal/service/session/catalog.go`: Session scope authorization.
- `go/core/internal/database/task_catalog*.go`: queries, publication, and decoding.
- `go/core/pkg/migrations/core/000008_task_catalog.sql` through
  `000013_task_catalog_workspace_uid.sql`: indexed columns, native tree aggregation,
  and workspace-incarnation filtering.
