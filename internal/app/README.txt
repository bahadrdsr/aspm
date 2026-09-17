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
Deployment packaging/provisioning, live GitHub authority, broader partitions,
key rotation and capacity qualification remain separate work.
