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

Selected Jira Cloud work items
=============================

Jira extends the same encrypted integration connections, finding delivery
outbox/history and DeliveryWorker. Admin-only configuration explicitly selects
jira-cloud-v3, an oauth2-bearer credential, lowercase cloud UUID, canonical HTTPS
API origin plus /ex/jira/{cloudId}, HTTPS browser siteOrigin, native project KEY,
decimal issueType and at most 16 customfield string mappings. No metadata,
provider permission, OAuth installation/refresh or endpoint is inferred.
Jira credentials reuse the v1 envelope and independent integration key with the
distinct aspm/jira-credential/v1 + NUL + workspace + NUL + connection AAD.
Slack's envelope and authenticated domain are unchanged.

Member reads expose Jira target configuration and permissionState:not-verified,
never a token. A writer's POST /api/v1/findings/{id}/delivery-previews with
{connectionId} is local review only. It performs no native I/O and persists no
intent. The server resolves only finding.id/title/severity, asset.name and the
trusted finding.deepLink into strings. Payload contains the canonical title,
severity/asset body and PublicOrigin finding link; it does not export raw
reports, evidence, notes, remediation or AI content. Summaries above 255 runes
are rejected without truncation. nativeValidation:not-run and explicit review
requirements do not certify native field support or Jira permission.

Jira enqueue requires {connectionId,idempotencyKey,previewDigest,confirm:true}.
The server recomputes the actor/workspace/finding/connection revision/target/
profile/payload digest and retains the selected user key in its durable binding.
Identical replay returns the original intent, including its terminal outcome.
Changed bindings conflict. Slack keeps its original two-field enqueue body.
Individual connection and delivery GETs remain resource-profile-typed.
Common connection and finding-history lists default to Slack only so the
existing strict Slack UI never receives Jira fields. Exactly one explicit
profile=slack-workspace-bot or profile=jira-cloud-v3 selects that profile.
Empty, unknown or repeated selectors are invalid. Profile/scope filtering
precedes totals and the unchanged ascending exclusive native-ID cursor.

For Jira, the common worker uses an explicitly approved direct verified-TLS
client bound to the selected API origin. The existing adapter first reads native
createmeta, bounded to four pages, 50 fields/page and 64 KiB/body, then sends at
most one native create. A separate fenced create_attempted_at is committed
before that POST. SQL slots, transactions and row locks are released throughout
metadata, create and response-body waits. Current role, enabled/revision/target,
credential identity, immutable binding and lease are checked at dispatch,
before create, while native I/O is held and before the terminal outcome commits.

Role loss, disablement or connection change cancels held context-honoring Jira
I/O. Before a possible create, revoked work is blocked with a specific reason
and no attempt marker. After a possible create, cancellation, lost ACK, malformed
receipt, timeout, 5xx or expired ownership stays uncertain without a receipt or
blind resend. Required-field failures retain sorted field IDs and metadata/
create stage; authentication and literal bounded Retry-After remain safe
terminal diagnostics. A confirmed remote URL comes only from the approved
siteOrigin plus /browse/{native issue key}, never a native arbitrary self URL.
Neither regrant nor reopen revives terminal work. Finding lifecycle, source,
verification, evidence and notes are unchanged.

V10 appends only nullable/default-free jira_target jsonb to the two common
tables and create_attempted_at timestamptz to finding_deliveries. It widens the
two existing named profile CHECKs to the Slack/Jira pair and adds the two named
profile_target CHECKs enforcing Slack/channel/null-target and Jira/empty-channel/
object-target tuples. Historical V1..V9 migrations, unrelated definitions and
old Slack values/null additions are preserved. UI/deployed-runtime setup, live
vendor authority, Data Center, status sync and broader recovery qualification
remain separate work.

Selected Teams Workflow notifications
====================================

Teams extends the same connections, local previews, finding delivery outbox and
DeliveryWorker with teams-workflows-channel. An administrator supplies the whole
write-only workflowUrl, declares channelType:standard and explicitly acknowledges
Workflow owner/co-owner continuity. That declaration is not verified ownership,
permission or channel identity. The common channel field remains empty.
Metadata exposes only the canonical HTTPS workflowOrigin and the declaration.
The entire original callback URL is encrypted with the existing independent key
and envelope, using aspm/teams-workflow-credential/v1 + NUL + workspace + NUL +
connection AAD. Exact URL/configuration no-ops preserve ciphertext and revision.

Admin/analyst preview is local, exports only canonical title/severity/asset/link,
and requires explicit queue consent with the current actor/revision/destination/
payload digest. The idempotency key is also bound into the stored intent.
Explicit Teams list selection never changes the default Slack-only lists or old
Slack/Jira DTOs, digests, credential domains and queue bodies.

Local admission and native Send share the adapter's exact Adaptive Card 1.4
serialization and 28 KiB byte cap. This is an adapter bound, not a universal
Teams platform limit. The worker preserves the complete escaped callback path
and raw signed query. It uses only an explicit direct normal-TLS client narrowed
to that origin, with no token header, proxy, redirects or alternate profile.
The existing reviewed-delivery authority/watch/fence guard commits the durable
attempt marker before POST, releases SQL locks during I/O, cancels held work
on authority changes and never blindly resends possible writes.

Native 202 is accepted with a NULL receipt, not confirmed or channel delivery.
Teams exposes outboundAttemptedAt for the shared create_attempted_at marker.
Failures never retain native messages, codes or secret URL components.
V11 adds only two nullable teams_target JSONB columns and replaces the four
existing profile/target CHECKs. Historical migrations and old values remain.
Teams UI, actual delivery-main origin policy, deployment and live account/channel
qualification are separate gates and are not provided by this backend increment.

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
Configuration reads alone start no prompt, job, tool or source/scan/verification
execution and never mutate a finding or workflow.

Durable read-only assessment orchestration is a separate implemented boundary.
Explicit reviewed-derived context previews and queue consent feed the independent
AssessmentWorker; results are advisory and do not change finding decisions.
The service environment and cmd/assessment-worker now consume that boundary.
See doc.go for immutable snapshots, current authority, one-attempt dispatch,
local I/O reservations, bounded shared admission and uncertainty semantics.
Owned native/runtime and UI-to-worker qualification is not live-model quality,
remote exactly-once processing, deployment isolation or whole-M11 completion.

Personal saved Work views
=========================

GET/POST /api/v1/work/views and GET/PATCH/DELETE /api/v1/work/views/{id}
manage preferences for the authenticated user in the selected workspace.
Every member, including a viewer, can manage their own views. Administrators
cannot read or change someone else's preferences. Existing session, membership,
Origin and typed error checks apply. Writes recheck current authority under
shared workspace/session/membership locks before locking the preference row.

POST requires name, query and sort, with no apiVersion or authority fields.
Name and query are trimmed, valid UTF-8, NUL-free and bounded to 256/512 bytes.
Name cannot be empty; query can. Sort is source-order, severity or title and
is only later loaded client-side presentation metadata. Saving stores no
finding rows and runs no search, intake, scan, AI or provider action.

PATCH and DELETE require the current positive decimal string revision.
Sparse PATCH preserves omissions. A canonical no-op preserves revision and
timestamps; a meaningful change advances revision once and updatedAt by at
least one PostgreSQL microsecond, preserving createdAt. Concurrent mutations
serialize on the owned row. Stale writes conflict; absent or foreign views
return the same opaque not-found error. Lists retain native ID pagination.

Work and CSV accept viewId instead of q. Empty, malformed or repeated viewId,
or q together with viewId, is invalid. An owned view supplies only its query
to the existing shared Work filter and page path. Saved sort never changes
server ID order, CSV columns, escaping, paging or completion trailers.

Schema v9 adds only the preference table and its owner/workspace/ID index
after published v8. Prior relation definitions and source/business rows are
unchanged. This backend does not claim a UI workflow, saved result snapshots,
cross-request consistency or a public membership-removal API.
