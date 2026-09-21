// Package app provides the M04-M07 local-account application API and import
// worker, plus M10 posture reports. Open(ctx, Config) returns Handler, Close(),
// ProcessImports(ctx), and the independently invoked ProcessReports(ctx).
// Hosts must serve HTTPS, or explicitly set PublicOrigin behind trusted TLS
// termination. BootstrapToken is an out-of-band, at-least-32-byte credential;
// no default account or password is created.
//
// M11 configuration routes persist AI profiles, workspace policy and explicit
// administrator egress grants. OpenAIConfigurationResolver owns a separate
// DB-only pool and resolves current actor/profile/policy/grant state in a
// read-only snapshot. Its private provider configuration is not an HTTP DTO or
// an execution reservation. Configuration APIs and resolution do no provider I/O.
//
// Config.AssessmentScope explicitly enables reviewed assessment preview, queue
// and cancellation writes. Empty leaves those writes unavailable while legacy
// APIs and historical assessment reads remain available. No scope is inferred
// from an API key. Schema V8 adds immutable previews, jobs and one durable quota
// scope; it does not rewrite V1-V7 definitions or legacy business records.
// Independently opened workers must use that same scope and coherent limits.
// Core construction, HTTP handlers and ProcessImports never dispatch assessments.
//
// POST /api/v1/findings/{id}/assessment-previews requires an explicit existing
// observationId, profileId, grantId, context and reviewed:true. A current admin
// or analyst must review/redact the derived UTF-8 context, which is nonblank,
// NUL-free and at most 32768 bytes. Its exact bytes are retained, not trimmed or
// repaired; the JSON body is separately capped at 256 KiB for escaped text.
// The original observation's evidence digest is source linkage, not the digest
// of that edited context. No raw report, note, unmapped field or S3 content is
// appended. Direct inclusion of the selected provider key is rejected, not
// silently redacted. This is not universal detection of unknown or encoded
// credentials; operator review and deployment data-processing approval remain
// necessary.
//
// The server fixes task finding-validity, data class finding-evidence, prompt
// revision finding-validity-reviewed-context/v1 and the reviewed-context ID.
// Context is untrusted data, never provider routing, instructions or approval.
// Preview consent lasts at most five minutes, shortened to the selected grant's
// expiry. POST /api/v1/findings/{id}/assessments requires previewId, an explicit
// bounded idempotencyKey and consent:true from that same current writer.
// Workspace/key replay retains the original requester/preview binding and
// receipt, but still requires current authority and unexpired consent.
// A later scan never replaces the approved context or selected observation.
//
// GET finding assessment history and GET /api/v1/ai/assessments/{id} expose
// workspace-scoped historical receipts to readers, including viewers, without
// granting execution permission. History uses native-ID pagination, default
// 100 and maximum 500. POST /api/v1/ai/assessments/{id}/cancel with {} is an
// idempotent current-writer operation, not permission to rewrite a terminal
// receipt. All routes use the existing cookie, Origin and workspace boundary.
//
// OpenAssessmentWorker accepts explicit Database, EncryptionKey, WorkerID,
// Scope, Client and execution limits. It owns an independent database pool
// and copied HTTP transport, not an Application, handler or S3 capability.
// Keyless local profiles need no encryption key; encrypted lookup never uses
// ambient credentials. The client must have a direct *http.Transport, no proxy
// or cookies, a positive timeout at most 30 seconds, and verified TLS 1.2+
// without hostname or DialTLS bypasses. Root CA ownership is copied. Each
// dispatch is constrained to the current approved origin/base; redirects,
// fallback and additional attempts are refused. The assessment-only request
// copy removes GetBody rewind authority before entering http.Transport, so
// internal HTTP/2 REFUSED_STREAM handling cannot replay its POST. HTTP/2 remains
// supported; standalone provider callers retain their existing behavior.
//
// Worker limits have no implicit defaults: input 1..32768 bytes, output
// 1..32768 tokens, response 1..128 KiB, request timeout positive and at most
// 30 seconds, lease 250ms..1 minute, authorization interval 10ms..1 second and
// shorter than the lease, concurrency 1..16, and 1..1000 requests in a trailing
// window of 1 second..1 hour. Scope/limit conflicts reject construction rather
// than create a second private budget. This is shared concurrency and request
// admission across profiles, workspaces and replicas, not token-rate, pricing,
// cost, account, model or region quota management.
//
// ProcessNext performs one scheduling step. Admission denial returns false,nil
// promptly; denied queued jobs and expired dispatches can instead be terminally
// settled without a request. Workspace authority precedes quota/job locks.
// The current requester role and exact profile/policy/grant revisions,
// capability, destination and expiry are checked before dispatch, during held
// I/O and at fenced finalization. Issuer demotion alone does not revoke a
// durable grant. Database clock_timestamp governs leases and admission; an
// earlier Resolve does not reserve either permission or capacity.
//
// Before the single native POST, a transaction commits attempts=1 and a
// dispatch-start marker. No connection is retained while awaiting inference
// or capacity. Completed, rejected, cancelled and uncertain marked attempts
// consume the trailing-window allowance once, without refunds or hidden retry.
// Concurrency instead follows a separate durable owner/attempt-token reservation,
// not public job state or the result lease. Cancel/expiry may retire a receipt
// while its local I/O is still alive. Only acknowledgment after response-body,
// socket and pending-dial cleanup releases that reservation early; an old token
// cannot finalize a retired receipt or release another attempt's capacity.
//
// The request's monotonic deadline starts before the dispatch-marker SQL, not
// after a delayed commit or Do call. The marker persists the same bounded budget
// using the database clock. Each attempt owns a copied transport and its sockets,
// enforces that fixed deadline on dialing and socket I/O, and fences late dials
// and resumed writes. Dial hooks must honor context; context-free custom Dial
// without DialContext is rejected. No SQL is held while draining/closing I/O.
// A dead owner's reservation stops consuming capacity at its persisted deadline;
// normally progressing database/local clocks and the trusted direct transport
// are required. That recovery bound does not assert remote compute or billing
// has stopped. Acknowledgment never refunds the marked request-window charge.
// These reservation columns are part of the unpublished V8 definition, not an
// upgrade from an earlier development V8 schema. Published V7 upgrade is additive.
//
// Before that marker, cancellation is known not-started; afterwards it is
// possibly-sent unless a response was received. Active authority loss cancels
// native HTTP and denies an advisory commit. Expired dispatches settle uncertain
// without resending; stale owners cannot overwrite terminal receipts. Close
// cancels owned operations and performs bounded finalization before pool cleanup.
//
// The existing providers.Assessor performs the native parsing. Schema, refusal,
// grounding, incomplete, tool, auth, rate, provider, timeout and bound failures
// are failures, never successful inconclusive fallbacks. Receipts retain safe
// native request/model/deployment/stop/retry metadata and actual usage when
// available, including cached input and cache writes. Missing or lost usage
// stays unknown, not zero spending. Native metadata is capped at 512 bytes,
// diagnostics at 1024, uncertainty at 8192 and context references at 16.
// Known credential echoes are rejected or omitted; raw envelopes are not saved.
//
// Supported, contradicted and inconclusive are advisory conclusions about the
// approved snapshot only. They never update findings, workflow, ownership, risk,
// notes, observations, assets or source state. There is no scanner, model tool,
// shell, target probe or execution proof. Owned native HTTP/TLS fixtures do not
// certify live model quality, retention, geography, an account or deployment
// egress. A local endpoint or proxy cannot prove local downstream processing.
// UI, worker command/packaging, deployment gates and broader M11 budgets remain
// separate integrations; this package does not claim complete M11.
//
// Optional Config.OIDC enables /api/v1/auth/oidc/start and /callback using
// go-oidc and oauth2. Discovery is lazy, HTTPS-only, endpoint-scoped, and bounded;
// provider failures leave local login available. Token/JWK responses are capped
// at 512 KiB and requests at five seconds, with redirects disabled. This slice
// accepts RS256 code flows with PKCE S256, verified email, exact issuer/audience,
// nonce, expiry, and signature checks. Client-secret POST authentication is
// preferred; explicitly advertised Basic-only providers are supported.
//
// Five-minute flows persist hashes of state, nonce, and the browser cookie in
// PostgreSQL. The PKCE verifier is held only in that HttpOnly/Secure host-only
// cookie, and a matching flow is atomically consumed before token exchange.
// Flow/session expiry uses Now; provider token expiry uses the actual clock.
// Issuer+subject is the identity key. First enrollment adds a viewer only to the
// explicitly configured existing workspace. Email matches never auto-link;
// group/role claims never assign privileges. Both login methods issue the same
// application sessions. OIDC-only accounts have no local password. Provider
// tokens and raw claims are not stored or logged; logout revokes the local
// application session, not the provider session. SCIM, SAML, advanced group
// mapping, account-linking UI, and third-party live IdP verification are separate.
//
// Schema ownership is explicit: the operator creates the PostgreSQL schema and
// S3 bucket. Versioned migrations own only app_* tables in Config.Schema.
// MaxConnections bounds this application's pool. M02 jobs/evidence schemas and
// behavior are independent. Reports use Storage.Prefix + workspace ID + "/".
//
// Core storage construction performs no HeadBucket probe. Raw reads delegate
// to evidence.OpenReader for the existing scope and stream-integrity checks.
// OpenImportWorker(Database, Storage, NormalizedPrefix) constructs only a
// database-backed importer with a read-only raw capability, not an auth/HTTP
// application. Its normalized output remains in PostgreSQL. OpenReportWorker
// accepts DatabaseConfig only and constructs no storage or auth component.
// These workers share the same database migrations and processing methods used
// by Application.ProcessImports/ProcessReports, without extra connection pools.
//
// Intake commits queued metadata only after immutable S3 publication. Manual
// processing never parses at startup or in HTTP handlers. Otherwise one serial
// worker drains the queue. Separate processes may call ProcessImports; claims
// use database-clock 90-second leases, monotonic fences, three attempts, and
// SKIP LOCKED. Each attempt has a 60-second work deadline, with atomic finding,
// observation, coverage, and completion commits. Parse failures are terminal;
// transient failures retry. ProcessImports waits for pending work within ctx.
//
// Upload preflight/finalization use short database transactions; S3 publication
// holds no connection or SQL lock. Finalization rechecks the authenticated
// session, selected membership, asset and immutable workspace/source/scan
// binding. Identical replay expires 24 hours after first server acceptance;
// client scan/collection times never renew that window.
//
// Imports persist their submitting session actor. Workers check that actor's
// current workspace write role before I/O, monitor it while reading, and lock
// and recheck it at finalization. Admin PATCH /api/v1/users/{id} changes only the
// selected membership, protects its last administrator, and durably fences
// pending/active imports on write-role revocation. Active S3 reads are cancelled;
// authorization-revoked is terminal and never changes prior findings/evidence.
// Pre-upgrade imports with no submitting actor cannot be silently authorized.
// The tighter source-scan unique index rejects ambiguous pre-upgrade bindings
// rather than merging or rewriting provenance.
//
// Reports are bounded to 32 MiB (8 MiB default), 5,000 findings, and 64 JSON
// nesting levels. Lists use limit/cursor. Finding histories return 500 records
// per page, with observationsNextCursor/notesNextCursor for the corresponding
// observationsCursor/notesCursor query parameters. CSV reads bounded pages
// without snapshot guarantees, escapes spreadsheet formulas, and exposes the
// X-ASPM-Export-Status trailer on completion or interruption.
//
// GET /api/v1/reports/overview accepts freshnessDays=1..365 (default 7) and
// aggregates one consistent database snapshot. Scanned assets have at least
// one successfully processed, successful complete full scan. Unscanned assets
// are not healthy-by-default. Freshness uses the last known source scan per
// source/scope/revision/branch, never collection time. Any stale assessed scope
// makes its asset stale; scopes without a known, nonfuture time are unknown.
// Stale and unknown are flags within scanned assets and may overlap. The UTC
// window is inclusive; source times before its start are stale. All findings
// contribute severity counts; openFindings includes open and in-progress work,
// including accepted risk or source-inferred resolution. Verified-resolution
// results are not integrated with canonical findings: the report explicitly
// labels verification not-run and counts no independently verified resolutions.
// Work totals and cursor pages also use one repeatable-read snapshot.
//
// POST /api/v1/reports/snapshots queues {name,freshnessDays}; GET on that
// collection lists bounded metadata pages, and GET /{id} reads saved results.
// ProcessReports drains only this queue, using database-clock leases/fences and
// three bounded attempts. It captures asOf from Now during generation, not
// submission, and persists aggregate data with its completion in a repeatable-
// read transaction. Completed snapshots are never recomputed. Open and
// ProcessImports do not start the reporting worker: hosts must invoke it.
// The reporting migration is additive and uses the existing application pool.
// SLA campaigns, full-scale performance, and HA readiness remain separate gates.
//
// Source inference never verifies resolution or changes human decisions.
// Accepted-risk expiry is computed using Now without rewriting the decision.
// Imported content is untrusted data; no scanner, script, or proof is executed.
// Retention/orphan evidence cleanup, merge/split, native connectors,
// independent verification, installer operation, and browser wiring are not
// implemented by this package.
package app
