// Package service provides the Environment and Run paths used by the real
// core-api, ingestion, and report-worker commands.
//
// Core/ingestion require explicitly supplied ASPM_S3_ACCESS_KEY and
// ASPM_S3_SECRET_KEY. Each process must receive its own scoped role credential;
// no operator or ambient AWS identity is discovered. Complete configuration is
// validated before PostgreSQL connections, HTTP probes, or listeners start.
//
// Core publishes raw evidence through the real application. Ingestion uses
// evidence.OpenReader for raw reads, owns no raw writer or auth handler, and
// commits normalized observations to PostgreSQL. ASPM_S3_NORMALIZED_PREFIX
// (default normalized/) must be valid and disjoint from ASPM_S3_PREFIX. No
// normalized S3 publication is currently required or performed.
//
// ASPM_S3_READINESS_KEY optionally names a caller-owned, nonsecret object within
// the raw prefix. Startup/readiness may HEAD that exact object with the supplied
// role key; they never HEAD/list the bucket. Without a key, readyz explicitly
// reports storage=not-probed and makes no credential-validity claim.
//
// Reports accepts database-only environment/configuration and uses
// app.OpenReportWorker: one pool, no storage client, password hashing, bootstrap,
// application handler, or HTTP/storage calls. Only health/readiness are served.
// Core/ingestion divide the total database budget between the integrity queue
// and application/import pool; reports uses its single pool's full budget.
//
// An existing assets directory without index.html supports API-only core
// startup with an explicit warning and 404 UI responses, never fabricated UI.
// Other startup failures and resource/shutdown failures propagate to callers.
//
// The legacy Check publication diagnostic remains operator-oriented and is not
// narrow ingestion readiness: it requires write/probe authority. Do not widen
// a worker key to run it; use the scoped readiness key instead. Runtime-role
// credential consumption does not establish deployed secret replacement,
// PostgreSQL role isolation, network policy, or HA/performance guarantees.
package service
