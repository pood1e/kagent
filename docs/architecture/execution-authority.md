# Execution authority

The controller owns durable Session authority, trusted task ancestry, and the
initiator's credential source. Actor identity, the Session's storage owner, and
initiator authority are separate values.

## Identity and scopes

`server.auth.mode: oidc` verifies the provider signature, issuer, API audience,
and expiry through the maintained `coreos/go-oidc` verifier. Principal IDs combine
the configured issuer and verified subject into an opaque identifier. Browser
sign-in, cookies, and browser-token refresh remain with oauth2-proxy and the IdP.

`executionAuthorization` enables native scope policy and encrypted credentials.
Its `scopeClaim` projects authenticated string or string-array attributes into
atomic scopes. Each `rules` entry names a resource, verbs, and alternative scopes;
an empty `anyScope` permits authenticated callers. The rule set is an allowlist
applied before the embedding application's resource and collection authorizer.

```yaml
server:
  auth:
    mode: oidc
    oidc:
      issuer: https://identity.example
      audience: kagent-api
      caFile: /etc/identity/ca.pem
executionAuthorization:
  scopeClaim: roles
  encryptionKeyFile: /etc/kagent-credentials/key
  oauth:
    tokenEndpoint: https://identity.example/oauth/v2/token
    clientId: kagent-delegation
    clientSecretFile: /etc/kagent-credentials/oauth-client-secret
    caFile: /etc/identity/ca.pem
  gatewayAudience: https://gateway.example
  rules:
    - resourceType: Session
      verbs: [get]
    - resourceType: Session
      verbs: [create, update, delete]
      anyScope: [execute]
    - resourceType: Agent
      verbs: [get, list]
    - resourceType: TeamMemory
      verbs: [get, list]
    - resourceType: TeamMemory
      verbs: [create, delete]
      anyScope: [knowledge-manage]
```

Complete the rule set for the resource types enabled by the installation,
including runtime `TaskStore` and `ToolInventory` access. Kubernetes resource
namespaces and collection filters remain the configured authorizer's boundary.
The explicitly selected `insecure` mode retains the source-less OSS default
when execution authorization is omitted.

## Durable delegation

`CreateSession.scope_ceiling` restricts the initiating principal's current scopes.
An omitted ceiling inherits them; a present empty ceiling grants an empty set.
The returned `Session.authority` records immutable initiator, parent, root,
ceiling, and credential deadline. Parentage comes from a verified live Actor
binding or the independently verified originating-Actor proof.

A child inherits the intersection of every ancestor's ceiling and deadline.
The chain is limited to 32 Sessions. Runtime collaboration stays within the same
initiator, storage owner, team namespace, and execution root. Dispatch admission
binds trusted task ancestry to the live originating task in PostgreSQL. A2A
message metadata remains application data.

`GetSessionAuthority` returns current inherited revocation; `GetRuntimeAuthority`
returns the calling Actor's effective scopes. Every execution check intersects
the immutable ceiling with the initiator's currently verified credential scopes.
`RevokeSessionAuthority` requires independent initiator authentication and queues
recursive native task cancellation. Credential revocation revokes that
initiator's existing roots and their descendants.

## Credential lifecycle

`RegisterDelegationCredential` exchanges a refresh token at the configured HTTPS
OAuth endpoint and verifies the resulting access token against the authenticated
initiator. The controller persists the official OAuth token representation with
AES-256-GCM and principal-bound authenticated data. The encryption key is exactly
32 random bytes supplied through a Secret file.

Unregistered callers capture their verified access token and inherit its bounded
expiry. Registered refresh credentials support background work and controller
restart. Refresh uses the official `golang.org/x/oauth2` client, a bounded HTTP
request, current client-secret contents, and verified TLS. A versioned database
lease serializes refresh across controller replicas; network calls run outside
database transactions. Rotated refresh credentials replace the stored version.
Permanent OAuth rejection revokes the matching source and existing authority
roots. Transient failures remain retryable. Recovery after provider rotation and
a crash preceding persistence requires fresh credential registration.

External automation can use an issuer-signed machine token with the API audience
and explicitly assigned scopes. Projected Kubernetes credentials retain the
separate `kagent-invocation` TokenReview boundary and their credential deadline.

## Session-bound callers

`GetDelegationCredentialStatus` reports whether the independently authenticated
caller has a verified renewable source. `IssueSessionCredential` issues an opaque,
authenticated-encrypted capability for an existing Session, with a requested TTL
of at most one hour and an expiry bounded by the native grant. Its ceiling is
fixed to the caller's current effective scopes. Credential refresh and grant
revocation remain native checks on every use.

Session credentials support Session reads, A2A input and cancellation, and access
to the Session's Agent. They stay bound to that conversation. Channels such as
voice can embed this capability in their own short-lived signed dispatch
metadata while the initiator's access and refresh tokens remain in the native
vault. The independent initiator owns credential issuance and revocation.

## Gateway authorization

`POST /authorize/runtime?scope=<configured-scope>` checks the current native
ceiling and revocation for an Actor. The gateway sends its audience-specific
Actor JWT as `Authorization`; gateway configuration owns the fixed required
scope. This endpoint has a dedicated verifier for `gatewayAudience`. The public
control-plane APIs retain the separate `kagent-runtime` Actor audience.

Successful decisions return HTTP 200 with `Cache-Control: no-store`. A gateway
combines this fresh dynamic decision with its selected-tool and static tool-tier
policies. Credentials remain inside the controller and the native egress path.

## Code navigation

- Contracts: `proto/kagent/api/v1/execution.proto` and `sessions.proto`.
- Credential source and scope rules: `go/core/internal/executionauth`.
- Live Actor binding: `go/core/internal/runtimeauth`.
- Atomic grants and cancellation: `go/core/internal/database/session_authority.go`.
- Collaboration: [MCP](mcp.md); team knowledge: [Memory](memory.md).
