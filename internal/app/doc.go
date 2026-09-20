// Package app provides the M04-M07 local-account application API and import
// worker, plus M10 posture reports. Open(ctx, Config) returns Handler, Close(),
// ProcessImports(ctx), and the independently invoked ProcessReports(ctx).
// Hosts must serve HTTPS, or explicitly set PublicOrigin behind trusted TLS
// termination. BootstrapToken is an out-of-band, at-least-32-byte credential;
// no default account or password is created.
//
// M11 configuration-only routes persist AI profiles, workspace policy and
// explicit administrator egress grants. OpenAIConfigurationResolver owns a
// separate DB-only pool and resolves current actor/profile/policy/grant state
// in a read-only snapshot. Its private provider configuration is not an HTTP
// DTO or an execution reservation. No AI inference or jobs are started.
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
