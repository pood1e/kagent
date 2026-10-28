# Memory

`MemoryService` authorizes a typed `MemoryScope` before accessing PostgreSQL or
embedding text. Personal collections pair an exact Agent identity with the
caller’s normalized principal. Actor calls derive that partition from their
persisted Session. Team collections use a namespace and are shared across its
Agents and authorized users; the configured Authorizer controls each operation.
Actor access is confined to its Agent namespace.

Text queries and writes use the controller’s `memory.model_config` reference to
an OpenAI-compatible embedding ModelConfig. The controller resolves live Secrets
or its configured rotating service credential file and requests 768-dimensional
vectors through the OpenAI SDK. A batch uses one embedding request and one atomic
store operation. Clients with their own embedding providers can supply vectors.

Harness `kagent.memory.teamNamespace` selects the collection used by ADK memory
tools and automatic memory extraction. Omitting it selects personal memory.

Code navigation:

- Contract: `proto/kagent/runtime/v1/memory.proto`
- Authorization and embedding: `go/core/internal/service/memory`
- Partitions and transactions: `go/core/internal/database/memory*.go`
- Schema: `go/core/pkg/migrations/vector`
