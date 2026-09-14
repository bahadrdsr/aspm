# M00 architecture contract

Status: proposed, pending independent review. The service boundaries below are
requirements for subsequent milestones, not processes or database tables shipped
by M00. The executable M00 tests validate design artifacts only.

## Deployment boundaries

```text
Browser (static React assets) / terminal installer
                    |
                 Core API
                    |
          durable commands + read models
                    |
    +---------------+----------------+------------------+
    |               |                |                  |
Ingestion        Background       AI service        Proof service
parse pool       separate         + approved        + isolated
reconcile pool   queue budgets    provider adapter  controlled fixtures
    |               |                |                  |
    +---- PostgreSQL metadata/jobs/receipts + shared S3 --+
```

One repository and one initial PostgreSQL cluster do not mean one process.
The ingestion service has independently deployable **parser and reconciler
roles**, each with its own process, queue admission, CPU/RSS and connection pool.
They are one ingestion domain, not two products. They must never run as expensive
background goroutines inside the interactive API.

Both Quadlet and Helm deploy the same topology. Parser replicas can increase
without scaling the API or the writer pool. Background reporting, connectors and
lifecycle work have separately capped queues rather than an unlimited mixed
pool. Independent AI/proof processes are reserved in the contract with zero
replicas until explicitly enabled and implemented in their later milestones.
No Node.js server runs in the customer topology.

## Table and service ownership

Logical schema/table names are design names, not migrations already created.

| Owner / proposed DB role | Owned writes | Deliberate boundary |
| --- | --- | --- |
| Core / `aspm_core` | `core.assets`, relationships, issue identity, workflow/disposition, notes, RBAC, approvals, saved filters and `core.outbox/inbox` | Only core changes human decisions; all commands recheck workspace permission and expected revision |
| Ingestion parser / `aspm_parser` | Scoped `ingest.import_attempts`, raw/normalized manifest staging and parser queue receipts | No core/decision writes; bounded transformations to S3 batches, not report blobs in jobs |
| Ingestion reconciler / `aspm_reconciler` | `ingest.scans`, observations, variants, affected instances, source projections, candidate proposals and `ingest.outbox/inbox` | Bulk persistence and versioned proposals/events; never writes core notes/dispositions |
| Background roles / separate `aspm_worker_*` roles | Their connector checkpoints, delivery receipts, exports and owned jobs/outboxes | Core-owned workflow changes through authorized core command handlers, never a broad superuser worker |
| Later AI / `aspm_ai` | `ai.jobs`, usage, grounded result/provenance and result outbox | Independent execution authority; reviewed results enter core through commands |
| Later proof coordinator / `aspm_proof` | `proof.jobs`, approved scope, environment snapshot, outcomes and artifact manifests | Fixture executors do not inherit API, connector, model or migration credentials |
| Migration owner / `aspm_migrator` | Versioned DDL and migration history | Exclusive migration lock; no application process has DDL or superuser rights |

Queue tables belong to their stage/owner schema. Each owner writes its own
outbox in the same transaction as domain changes. Consumers own an inbox/receipt
with a unique delivery key and enforce expected revisions before an effect.
Shared-store access is explicit: core queries approved ingestion read models
with read grants or a versioned query boundary, not arbitrary source-table
updates. Asset/canonical identity allocation and correlation proposals use
bounded batches rather than a network call per observation.

Workspace identity accompanies every query, job, receipt, download and export.
Any shared views or row policies are defense in depth, not substitutes for
service authorization. No external adapter receives access to another service's
schema or a database superuser credential.

## Ingestion and job semantics

1. Core authorizes a bounded upload session and source/workspace/scope identity.
   A streaming intake endpoint or scoped direct object upload transfers bytes
   outside interactive handlers. Enforce format, depth, record, report, archive
   and per-workspace byte limits. Do not buffer a whole report in the API.
2. Stage immutable raw content in shared S3, record byte count and SHA-256, and
   verify availability/integrity before committing its database manifest and
   parsing job. Acknowledgment means a durable retrievable reference and job,
   not completed parsing or fresh findings.
3. Parsers stream bounded versioned normalized batches to S3. Queue compact
   integrity-checked batch references, not reports or one message per finding.
   Preserve original values and unmapped fields separately from projections.
4. Reconcilers perform indexed candidate blocking, batched lookup and
   COPY/staging/merge transactions. Avoid global all-pairs correlation and
   per-record SQL round trips. Writer admission depends on database headroom.
5. Commit observations, projection revision, batch receipt and outbox together.
   Finalize a run only when its exact required manifest membership is
   reconciled. Source completeness and local processing completeness are
   independent, including when a failed scanner report is locally processed.
6. Core consumes source/correlation proposals with current workspace/evidence/
   decision/policy revisions. A proposal cannot replace an accepted risk,
   suppression, note or human workflow state.

Every job envelope carries a schema version, job/batch ID, workspace, source,
scan and scope identity/revision, import attempt, parser/normalization revision,
input object reference/digest, stage, trace ID, attempt count and deadline.
Do not include credentials, raw reports or model-proposed executable commands.

Delivery is **at least once**, with **idempotent business effects**. Never claim
end-to-end exactly once. Use database time for leases, bounded claims,
heartbeats and monotonically increasing fencing tokens. A worker that loses
its lease cannot renew, publish a final manifest or finalize a run with an old
fence, even after its external work finishes.

Initial timing hypotheses: 30-second leases, 5-second heartbeat/status sampling,
bounded backoff and an overall job deadline. A controlled missing-progress/
heartbeat scenario must become visible within the frozen 15-second stall gate;
long but healthy work needs real counters and stage-specific explanations.
Crash-before-commit, crash-after-commit-before-ack and duplicate deliveries use
the same stable receipts. Ambiguous external delivery remains **uncertain**
until reconciled; blind retries are not idempotency.

Observation identity is logical scan/variant/affected-instance membership.
`importAttemptId` tracks attempts, not another observation identity. Changed
parser output creates a normalization revision without manufacturing a scan.
Keep a deliberately unpartitioned identity/receipt registry, or include partition
identity in all unique constraints; do not assume partitioned PostgreSQL has a
global unique index independent of its partition key.

## Shared resources and admission

The small contract has four enabled roles, each with ten connections:
`4 * 10 + 10 maintenance = 50`, within the global maximum of 80.
Zero-replica AI/proof pools have zero connections. The remaining 30 are
headroom, not permission for every new replica to assume an 80-connection pool.

The future resolver must sum every replica, rollout overlap, all background
subpools and maintenance demand against the configured **and actual** database
allowance. Schema validity cannot implement this cross-field arithmetic.
Count migration/health/monitoring sessions and external consumers; require
zero surge or enough reserved capacity during upgrades. Pool wait has a timeout
and visible backpressure. Release connections during object IO, provider waits
and remote delivery; never hold a domain transaction over inference.

Enforce bounded queued bytes, records per batch, retries, workspace backlog and
destination concurrency. Schedule work fairly by workspace and stage. Retain
admitted work durably; reject or defer new work with a reason when capacity is
unavailable. More replicas cannot add upstream API quota, database bandwidth or
storage capacity.

AI shared admission keys reflect provider account/project/model-class/region/
deployment limits, not only API keys. Snapshot profile/policy revisions so an
edit cannot silently reroute approved work. Disabled mode performs no inference;
local-only/offline mode denies hosted egress on **both** workers and runtimes.
Model tool requests are untrusted data, never execution permission.

## Evidence storage and consistency

Shared, authenticated S3 is mandatory for independently rescheduled workers.
Node-local report storage is not the production default. An external store is
operator-owned and must pass the same selected compatibility checks.

Managed SeaweedFS community 4.47 has these explicit mini-profile overrides:
`-master.telemetry=false`, `-admin.ui=false`, `-webdav=false`,
`-s3.port.iceberg=0`, `-s3.port.lance=0`.
The source declares those switches in
`https://raw.githubusercontent.com/seaweedfs/seaweedfs/4.47/weed/command/mini.go`.
Only the S3 API may be available to authorized application workers. All admin,
master, filer and volume HTTP/gRPC interfaces remain internal and unpublished.
UI disablement does not stop every management service. Bind/network-policy/
firewall and egress enforcement must be exercised at M02; these flags alone
are not readiness or isolation evidence. Mini is a small-install candidate,
not an enterprise sizing or HA assertion.

Use protected service-specific credentials and workspace-scoped evidence
authorization. Never expose root S3 keys to browsers, public buckets or the
untrusted parser's input. Scoped upload/download sessions are short-lived and
cannot widen their workspace/key or bypass size limits.

There is no distributed transaction between PostgreSQL and S3. Content is
staged, checked, then referenced by a committed manifest. A crash may leave an
unreferenced object, but must not leave a success-shaped reference to absent
content. Garbage collection waits for an explicit grace/ownership check and
preserves objects still referenced by active or held evidence. Downloads
distinguish available, archived, expired, missing and corrupt states.

Before real data adoption, implement a consistent database/object-manifest
backup epoch, configuration/reference export and separately protected encryption
material recovery. A small-install backup can quiesce writers explicitly.
Restore into fresh owned resources and verify digests, history/decision links
and a cross-service job. A database-only dump or empty-schema restore is not
the acceptance gate.

## Lifecycle and progress

Keep source scan time, collection time and import time separately. Unknown
source time is null. A poll/reimport cannot refresh scan age. Scope includes
the scanner configuration/revision, branch/ref or inventory boundary needed
to compare meaningfully.

Only a newer successful complete full same-scope scan with every required
local batch reconciled can infer absence-based **source** resolution. Failed,
partial, delta, stale or different-scope absence cannot. Scanner inference is
not independently verified resolution. Non-reproduction remains inconclusive
analysis, not automatic false-positive disposition. `lifecycle.contract.json`
contains twelve synthetic snapshots and the exact raw-report digest.

Expose accepted, parsing, partially processed, reconciled and failed states,
with observed bytes/records, queue age, stage duration and projection lag.
Track endpoint connectivity, permissions, collection, parsing, enrichment and
reconciliation separately. An HTTP health check does not imply current findings.
Do not fabricate percentages, tween critical risk values or animate stalled
jobs toward success. Metrics must avoid raw content and unbounded identity
labels.

Retention classes and positive bounds are in `capacity.profiles.json`.
Preview expiration/archival, exempt held evidence and preserve active decisions,
run/variant membership and labeled availability. Stable issue counts do not
imply stable history/index/WAL growth.

## Deferred implementation and evidence

No database migrations, job engine, chart, Quadlet unit, provider call or
executor is implemented by M00. Stateful HA, disconnected readiness and
enterprise capacity remain unverified. The schema's `x-semanticValidation`
requirements need executable resolver/admission checks at M03/M11, including
reference graph resolution and egress enforcement.

Relevant primary design references:
`https://www.postgresql.org/docs/current/populate.html`,
`https://www.postgresql.org/docs/current/ddl-partitioning.html`,
`https://www.postgresql.org/docs/current/sql-select.html`.
The M00 test-author mapping and independent reviewer, not this document, own
acceptance of later executable behavior.
