Application-owned Slack remediation
==================================

Core accepts an optional independent 32-byte IntegrationEncryptionKey. The
core service reads ASPM_INTEGRATION_ENCRYPTION_KEY once as canonical standard
base64. Unrelated keyless startup is supported; creating a connection is not.
The key is not derived from database, bootstrap or storage credentials.

Admin-only integration connection writes persist AES-256-GCM credentials with
a fresh 12-byte nonce and authenticated workspace/connection identity. Member
reads expose metadata only. Meaningful changes advance the connection revision.
Credential existence is not vendor verification. Key rotation/provisioning is
not qualified, and an unauthenticatable old credential is never silently read
under a different identity.

Admin/analyst POSTs to a finding's deliveries collection persist an immutable
canonical notification and its actor, connection revision, destination and
workspace-scoped idempotency binding. Identical replay returns the original
durable result. A changed binding conflicts; GET is the way to inspect an old
result. Neither connection writes, enqueue nor report intake performs delivery.
Delivery records retain their finding identity even if inventory is later
deleted; a delivery does not rewrite finding workflow, risk or source evidence.

OpenDeliveryWorker owns a separate PostgreSQL pool, explicit key, bounded lease,
worker identity and approved HTTP client. It has no core HTTP/auth handler or
S3/bootstrap dependency. Slack uses its fixed HTTPS API base unless trusted
worker configuration explicitly selects an HTTPS gateway. API and source
payloads cannot select a gateway, token, approval or native headers.

ProcessNext handles at most one queued or expired-dispatch record. Current
writer membership and enabled, unchanged connection configuration are locked
and checked before committing the fenced dispatch-start marker. All SQL locks
and connections are released before the unchanged native adapter performs I/O.
That committed marker is the authorization linearization boundary, not an
atomic transaction with Slack. A later disable/revocation/cancel cannot promise
that the provider saw nothing.

Only a live matching owner/fence records a native outcome. Canceled or expired
possible writes remain uncertain, never eligible for a blind fresh POST.
Persisted failures and Retry-After are terminal data, not instructions to retry
or sleep. There is no automatic resend, retry endpoint or exactly-once claim.
Close cancels this worker's active contexts and waits for bounded finalization.

Connection/send UI and the standalone delivery command now consume this API.
Their owned HTTPS/TLS qualification is separate from this backend's PG/API
acceptance. Deployed worker scheduling, live Slack authority, production key
rotation and broader partition/recovery qualification remain separate work.

Selected GitHub source collection
================================

Sources support one explicitly selected owner/repository through the existing
github-cloud-app connector and a supplied installation bearer. Admin connection
writes use the same optional independent encryption key, but source credentials
have the separate aspm/source-credential/v1 + profile + workspace + source AAD.
Slack's credential domain is unchanged. Metadata reads never return secrets.

An explicit writer request creates an immutable source/revision/repository/actor
binding. Replays with the same binding return the durable job; changed bindings
conflict. Core CollectionStorage is an optional, separate read-only evidence
capability, not an upload or bucket-probe capability. Missing collection storage
prevents enqueue without breaking existing core startup.

CollectionWorker has its own DB pool, explicit scoped storage publisher, key,
bounded lease and approved normal-TLS client. Source transports are direct and
origin-bound; they do not inherit ambient proxy/credential configuration.
Publication reuses the existing bounded app put path. Reads reuse evidence.Reader
with an explicitly owned transport and verify size/digest before any successful
evidence response. Existing evidence.Open and default reader behavior are unchanged.

Each exact native repository body and alert RawMessage is published separately.
PG stores bounded metadata and evidence.Ref only. A collection is bounded to the
native configured request/page/record limits and at most 32 MiB accepted raw bytes.
Completion means feed exhaustion, never a scan, normalized finding or verification.
Record ordinals preserve zero-based native arrival order; pagination uses real IDs.

The worker watches current role/source/revision/lease during native and storage
I/O without retaining a SQL slot. Finalization rechecks authority and ownership,
locks workspace before membership, serializes the stable repository identity,
and commits the asset/link/record metadata in one transaction. The final update
uses the actual PG clock after inserts; expired provisional writes roll back.
Expired work is settled terminally, not sent through hidden collection retries.
Unreferenced objects from interrupted publication are not committed evidence.

The first asset is repository/full_name/medium/unassigned with empty environment
and tags. Later collections preserve all human asset fields. A stable upstream
ID reuses the scoped asset across sources and reopens; a pinned source cannot
silently rebind to a different repository ID. Partial collections preserve only
valid authorized repository/alert records, gaps and safe native failure metadata.

The Sources UI and standalone collection-worker now consume these APIs. Their
owned HTTPS/core/worker/storage qualification is separate from this backend's
transaction and upgrade acceptance. There is no organization discovery, bearer
refresh, code execution, scanning or finding normalization in this slice.
Image and opt-in deployment artifacts consume this backend without automatically
provisioning credentials or starting collection. Installer provisioning, live
GitHub authority, broader partitions, key rotation and capacity qualification
remain separate work.

Persistent AI configuration foundation
=====================================

M11 setup adds selected-workspace GET/POST /api/v1/ai/profiles and GET/PATCH
/api/v1/ai/profiles/{id}, GET/PATCH /api/v1/ai/policy, GET/POST /api/v1/ai/grants,
GET /api/v1/ai/grants/{id}, and POST /api/v1/ai/grants/{id}/revoke. All routes use
the existing protected session and workspace selection; mutations also require
the existing Origin check and a transactionally rechecked administrator.
Members may read metadata. Lists use limit=1..500 (default 100) and ID cursors.
Bodies are bounded to 32 KiB and reject undeclared fields.

Profiles explicitly select openai, azure-foundry, anthropic or local, a name,
endpoint, model, enabled boolean and structuredOutput review boolean. Name,
model and deployment are bounded to 256 UTF-8 bytes; endpoint and API key to
16384 bytes. Foundry requires a separately supplied nonblank deployment. Its
value may match the model; neither field is inferred from the other. Other
families use an empty deployment. Hosted credentials are required.
Local credentials may be omitted or null. Empty keys are invalid. PATCH retains
omitted fields, including credentials; null clears only a local credential.
Any meaningful change, including name/disable/re-enable, advances the server's
string revision. A public no-op retains it. A supplied nonempty key always
replaces the credential and advances revision, without comparing secret bytes.

Credentials reuse the optional independent IntegrationEncryptionKey and shared
v1 AES-256-GCM envelope, with fresh nonces and the distinct authenticated domain
aspm/ai-credential/v1 + NUL + workspace + NUL + profile. Slack and source domains
and key handling remain unchanged. Storing a key without encryption capability
is unavailable (503); keyless local configuration requires no encryption key.
Metadata exposes credentialConfigured, never a key, envelope or native header.
No ambient provider, bootstrap, database or storage credential supplies an AI key.

Endpoint validation is pure and never probes or resolves a provider/catalog.
Hosted families require HTTPS even on loopback. Local HTTP permits only literal
private/loopback IPs, not localhost or public HTTP; local HTTPS may use an
explicit hostname. Userinfo, queries/fragments, invalid ports, raw/decoded
controls, path traversal, encoded separators and nested escapes are rejected.
Reviewed destination strings are stored and compared exactly, including a
trailing slash. Existing provider protocols and TLS/proxy behavior are unchanged.

Unconfigured policy is deny-only disabled metadata at revision "0", with null
actor/time and no persisted authority. PATCH accepts only mode. Meaningful
changes persist the server actor/time and advance revision; no-op changes do not.
local-only requires family local and no grant, even for a hosted family pointed
at loopback. approved-hosted requires a real matching grant for every family.
Grant creation asserts the current profile ID/revision, policy revision, exact
destination, finding-validity task, finding-evidence class and explicit future
RFC3339 expiry. The server checks these assertions, generates the scoped ID and
records the issuing admin. Expiry is never defaulted or extended; precision
beyond PostgreSQL microseconds is truncated earlier. Revocation requires {} and
preserves its first server actor/time on repeated calls.

finding-evidence means potentially sensitive application/repository evidence,
including finding metadata, source locations and untrusted bounded code/evidence
excerpts. It is not a certification of public/redacted data. Configuration keys
are never approved payload. This foundation reads or transmits no such evidence.
A grant is a durable workspace decision: issuer demotion alone does not revoke
it. Explicit revocation, expiry or changed profile/policy revision invalidates it.
Changing policy mode or disabling/re-enabling a profile cannot revive an old grant.

OpenAIConfigurationResolver(ctx, AIConfigurationResolverConfig{Database,
EncryptionKey}) owns a separate DB-only pool and optional cipher, with Close().
Resolve(ctx, AIConfigurationRequest{WorkspaceID, ActorID, ProfileID, GrantID, Task,
DataClass}) uses one read-only repeatable-read snapshot. ActorID is trusted
server/worker identity, never a public actor override. Every lookup checks current
admin/analyst membership and all current profile/policy/grant bindings before
decryption. Denials return zero configuration and a safe policy/capability error;
database failures are safe unavailable errors. The private providers.Profile and
Policy authorize only one workspace/task/class/exact destination, no fallbacks,
and only a verified stored grant ID as ApprovalRef. HTTP never returns this result.

AI writes lock workspace before session/membership, and profile before policy
before grant where needed. Sparse patches serialize without losing omitted fields.
Schema v7 is additive after the exact published v6 schema, and reopens do not
repeat it. Schema-only upgrade evidence makes no old customer-data migration claim.
Resolve is a point-in-time configuration check, not a future action reservation.
Later worker work must re-resolve separately. The AI settings interface now uses
these configuration APIs. A separate owned HTTPS/core/PostgreSQL workflow covered
four provider families, matching Foundry model/deployment names, finite grants,
revision invalidation and revocation with zero provider requests and a final
disabled policy. This is not inference or provider-account qualification.
There is no prompt, assessment job, tool, source/scan/verification execution or
finding/workflow mutation in this foundation, and no M11 execution/completion claim.
