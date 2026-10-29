package a2a

// RuntimeIdentityPath is projected by Substrate and rebound on restore. It is
// routing metadata, not an authentication credential.
const RuntimeIdentityPath = "/run/kagent/identity/name"

// DispatchHeader carries the gateway's attempt fence to the runtime TaskStore.
const DispatchHeader = "x-kagent-dispatch-id"

// AuthorityUnavailableReason identifies a rejected runtime save whose grant is
// revoked or expired. The runtime may attach a safe terminal explanation.
const AuthorityUnavailableReason = "KAGENT_EXECUTION_AUTHORITY_UNAVAILABLE"
