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
