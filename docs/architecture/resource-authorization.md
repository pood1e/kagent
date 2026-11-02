# Resource authorization

`AuthorizationService` exposes the native permission catalog, current-principal
checks, and direct role bindings in `proto/kagent/api/v1/authorization.proto`.
Its implementation lives in `go/core/internal/authorization` and uses the official
OpenFGA Go SDK with a pinned store and immutable model ID.

## Ownership

| Data | Owner |
| --- | --- |
| Canonical principal and issuer-qualified groups | Native authentication |
| Workspace membership and direct role bindings | OpenFGA |
| Namespace and resource UIDs | Kubernetes API reader |
| Session initiator, workspace UID, and Task ownership | Native PostgreSQL store |
| Mutation intent and confirmation | Native authorization audit ledger |

The resolver constructs structural contextual tuples from these trusted resource
facts. Workspace membership intersects named-principal access. Session ownership
uses `SessionAuthority.initiator_id`; `workspace_uid` pins the original namespace
incarnation. Task keys hash the canonical `TaskReference`. Group memberships use
verified token claims qualified by their issuer.
Harness, model-connection (`ModelConfig`), and MCP-server (`RemoteMCPServer`)
scopes use their current Kubernetes UIDs. An MCP tool scope hashes its server's
UID-bound object key and exact, case-sensitive tool name. A server use binding
covers its tools; a tool use binding covers that name within one server
incarnation. Model credentials retain their Secret boundary.
`ToolService` checks server read/manage actions for catalog and connection
operations, and the exact tool-call action for MCP Apps. Permission checks precede
outbound calls. The authorized server UID accompanies resolution and deletion;
catalog projection also verifies the current UID and observed discovery generation.
The application injects its configured native authorization engine into this
service. Engine configuration enables its protected resource operations.
Namespace-scoped installations include a namespace-reader ClusterRole restricted
to `get` on the configured namespace names, providing their stable identities.
`ListWorkspaces` enumerates observed namespaces, checks `member` against each
current namespace UID, and returns authorized name/UID pairs. Its bounded scan
may return an empty page with a continuation token.

Scoped checks and binding RPCs carry their workspace context. Checks return
allowed actions in request order. Binding reads and mutations require their own
permission; Session owners may share read access to their Session or Task.
Bound runtime and share credentials use their dedicated native entry points.

## Model and consistency

`model.fga` is the model source; `model.json` is its embedded generated form.
`make authorization-model-check` uses the official CLI to check drift and run
`model.fga.yaml`. Go tests verify that every permission and role maps to the model.
Startup reads the configured model and verifies semantic equality with the
embedded contract. Configure `ControllerConfig.authorization` alongside OIDC
authentication; use HTTPS and a credential file for remote OpenFGA endpoints.

Checks use `HIGHER_CONSISTENCY`, bounded batches, and caller cancellation. Engine
failures return `Unavailable`. Each mutation first persists its immutable audit
intent, then writes the binding and a unique operation marker in one OpenFGA
transaction. Readback confirms uncertain commits. A repeated request ID retains
the original result, including after a later revoke. PostgreSQL transactions end
before network calls begin.

Related execution identity and delegation rules are described in
[execution authority](execution-authority.md).
