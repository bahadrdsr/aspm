# M02 durable services

Status: independently reviewed runtime increments and real local integration
evidence. Remaining authorization, deployment and platform/capacity gates are
separate. This document distinguishes the original M02 integrity pipeline from
later authenticated application services; it is not milestone or release closure.

## Running components

- `cmd/core-api` serves the built UI and separate liveness/readiness endpoints.
- `cmd/ingestion` runs durable import processing independently of the UI, alongside
  the original deterministic byte-integrity check pipeline.
- `cmd/report-worker` processes persisted report snapshot jobs using PostgreSQL
  only. It has no storage credential requirement or application/auth API.
- `internal/jobs` owns its PostgreSQL table, scoped enqueue receipts, bounded
  claims, server-clock expiry, fencing, retry state and completion receipts.
- `internal/evidence` uses authenticated S3-compatible storage. Writes stage in
  a restricted temporary file to bound memory; no partially read input becomes
  an acknowledged object reference. Reads verify length and SHA-256 rather than
  treating an ETag as evidence integrity.
- `internal/ingestion` joins these real components for deterministic integrity
  jobs. It does not execute reports, scanner commands or model-supplied tools.

All roles need `ASPM_DATABASE_URL`. Core and ingestion additionally need explicit
`ASPM_S3_ENDPOINT`, `ASPM_S3_ACCESS_KEY`, `ASPM_S3_SECRET_KEY`, and
`ASPM_S3_BUCKET`, supplied through protected role-specific process environments
or orchestrator secret references. Their access keys and signing secrets must be
distinct from each other and from the storage operator. Missing required storage
credentials fail before database or HTTP I/O. Do not commit credential values.

Use `ASPM_S3_PREFIX` for the selected raw evidence scope and
`ASPM_S3_READINESS_KEY` for an operator-preseeded, nonsecret object inside that
scope. A scoped ingestion reader does not need bucket-wide HEAD/list permission.
`ASPM_S3_NORMALIZED_PREFIX` selects ingestion's separate publication scope.
The reviewed SeaweedFS policy grants core raw/approved reads and writes,
ingestion raw reads and normalized writes, and an AI identity approved reads
only. Policy generation rejects missing/reused role credentials and overlapping
or unsafe prefixes. AI application/job integration is still incomplete.

Reporting needs no `ASPM_S3_*`, `AWS_*` or `ASPM_BOOTSTRAP_TOKEN` values.
Only core consumes bootstrap/public-origin configuration and serves the
authenticated application. `ASPM_SCHEMA`, `ASPM_DB_MAX_CONNECTIONS`,
`ASPM_LISTEN` and core's `ASPM_ASSETS` select deployment context. Each replica's
pool is bounded; operators must budget connections across the replica count.

The services require an existing database and S3 bucket. By default, startup
initializes only the selected database schema/table and requires any configured
readiness object to exist. A failed dependency does not fabricate readiness or
fall back to memory/filesystem state.

Core can explicitly opt into preparing its selected nonsecret readiness object
with `ASPM_S3_PREPARE_READINESS=true`. This is off by default and is rejected for
ingestion/reporting. Core uses only its existing raw-scope credential and a
conditional create, then confirms readable-object access. Existing bytes are
not overwritten, permission failures remain failures, and no bucket-listing or
Admin permission is added.

`GET /healthz` reports process availability. `GET /readyz` checks the dependencies
appropriate to the selected role, using the scoped readiness object when
configured rather than requiring bucket-wide access. Reporting checks the
database only. Pipeline progress must still be inspected separately. The actual
core serves authenticated application APIs; the separate M01 `aspm-dev` static
host continues to return explicit unavailable responses for data APIs.

The legacy `ingestion --check` command is an operator-only integrity diagnostic:
its full evidence-store startup requires bucket-wide permission. It is not a
readiness workaround for the restricted core or ingestion identity. Never widen
an application role's permissions to make that command pass. With a separately
authorized operator configuration it writes one explicit synthetic object/job
and waits for an independent worker's receipt; it does not process its own job,
test an external target, classify a finding or mark a real scan complete.
The small system-check history is retained. Use the authenticated import and
snapshot flows to qualify the actual restricted application roles.

## Validation recorded on September 15, 2026

The independent test-author suite was first run RED against working fixtures:
the PostgreSQL/S3 fixture gate passed and the eight production gates failed only
because their bindings were missing. The forwarding-only binding was added
after implementing the real components; original expectations were not weakened.

The suite then passed using PostgreSQL 18.6 and SeaweedFS community 4.47:

- Scoped concurrent idempotent enqueue, immutable envelope conflicts and reopen.
- Exclusive claims and the configured connection/lease bounds.
- Heartbeats, expired-owner rejection, newer fences and idempotent completion.
- Actual child-process death followed by committed-work recovery.
- Bounded retries and explicit final-lease exhaustion.
- Exact remote bytes across separate clients and OS processes.
- Corruption, missing objects, cross-workspace/prefix access and failed writes.
- Real deterministic integrity success/failure and empty-queue behavior.

Independent review added regressions for lowering a worker's configured retry
cap after jobs already exist. Claim, reclaim, retry and exhaustion now enforce
the lesser of the job's recorded limit and the worker's configured limit without
rewriting the original envelope. All added lower-cap and per-job-limit cases pass.

Run with `go test -tags=integration -mod=readonly -count=1 -timeout=120s
./tests/integration` and the protected `ASPM_TEST_*` variables described in
`tests/integration/CONTRACT.txt`. The Windows helper reads a supplied private
runtime metadata file and redacts credentials; missing fixtures fail rather than
skip.

Separate native Windows service processes answered readiness and served the
built UI. An independently submitted check completed through the running worker.
This is a development validation path, not native Windows production support.

The owned Helm chart linted/rendered and was installed on an isolated Kubernetes
1.36.4 two-node lab with managed PostgreSQL/S3 and separate core/two ingestion
replicas. Workers were scheduled on both nodes and a check submitted from the
core pod completed with a remote-storage-backed receipt. The lab control-plane
taint was removed only to make the second test node schedulable.

Subsequent local processes use distinct restricted core/ingestion S3 identities
and a database-only report worker. A real HTTPS login, import, independent
processing, report snapshot and logout flow passed after replacing the main
process credentials. Separate isolated-store checks exercised 48 actual
operations, including 35 native permission denials on existing objects or
disallowed writes. These checks do not certify PostgreSQL roles, network
isolation or general AI workflows.

The ten later backend security/correctness corrections received independent
source acceptance with the original controls unchanged. All ten gates and the
19 original application gates passed against real PostgreSQL/S3, followed by
the three real-main-role gates and all 48 storage operations. The reviewed code
was then built as native Windows processes and Linux application-role binaries.
The refreshed local processes passed the actual HTTPS API and browser flows,
including independent import/report processing and logout revocation.

The owned Kubernetes lab was also upgraded to the reviewed runtime. It now runs
one core, two ingestion replicas on different nodes, and one database-only report
worker. Separate core/ingestion Secret values were verified against the exact
keys exercised directly against that lab's S3 service. All four application pods
use the selected role references; no old shared-key application pods remain.
The storage policy was reloaded without changing the existing operator/database
credentials or replacing persistent volumes.

The selected nonsecret readiness object was prepared with a conditional write;
an existing different object is not overwritten. Direct scoped reads and native
permission denials passed, including unchanged bytes after a denied write.
A real bootstrap/login, asset, queued import, independent reconciliation,
queued snapshot and logout flow then passed against the Kubernetes application.
Its HTTPS path used an explicitly owned loopback TLS termination fixture and a
real Kubernetes port-forward, not mocked API responses. This does not qualify a
production ingress controller, native Linux/systemd execution or network policy.

## Deployment artifacts and limits

`deploy/helm/aspm` has independent replica/resource settings and supports managed
or externally supplied PostgreSQL/S3. `existingSecret` selects the database and
core-bootstrap inputs; it is not a runtime S3 fallback. Managed storage requires
its separate `storage.policySecret.name` and `storage.policySecret.key`
selection, mounted as `s3.json` only in the storage container. Missing policy
selectors fail rendering instead of falling back to the application Secret.
`core.s3Secret` and `ingestion.s3Secret` each select a required secret name,
access-key key and secret-key key. Role-specific `rawPrefix`, `readinessKey`
and ingestion's `normalizedPrefix` select the storage scope. Reports receives
database settings only. Values contain references, not credential values.
Internal services do not publish database/S3 ports externally. Enabling ingress
requires a host and TLS Secret.

`core.prepareReadiness` is a strict boolean, defaulting to `false`. Only an
explicit `true` enables the core preparation flag; the rendered environment
value is a string. Ingestion and reporting never receive that flag. Shipped
Quadlets do not impose inline overrides, so an approved installer can opt in
through the protected `core.env` without changing worker authority.

The role-specific Helm references have independent source acceptance and real
rendering gates. Rendering alone cannot prove that selected Secret keys exist,
contain distinct correctly scoped credentials, or match the policy/readiness
object. The separate owned-cluster qualification above covers that selected lab
configuration, not arbitrary operator-supplied environments.

`deploy/quadlet` declares separate Linux/systemd services, private container
networking, persistent database/storage volumes and external credential files.
Core, ingestion and reports require their separate `core.env`, `ingestion.env`
and `reports.env` files. The pure Go `internal/install/quadlet` renderer takes
explicit nonsecret scope selections, preserves unrelated unit directives and
rejects missing/unsafe scope, shared-file fallback or report storage inputs.
It does not resolve credentials, run tools or grant installation approval.
The selected-scope output passed the actual Quadlet generator; that is syntax
and generated-command evidence, not service activation.

The core binds loopback by default. The real Ubuntu Quadlet 4.9.3 generator was
extracted into a project-local ignored tool directory and used without
administrator installation. It validates the unit syntax and generated commands.
Image stages explicitly clear `ENTRYPOINT` and use `CMD` for the core default,
so the worker unit's `Exec` correctly selects ingestion. This avoids requiring
the newer Quadlet `Entrypoint` key, which that supported-distribution generator
rejects. An isolated container run using the generated command arrangement
reported the ingestion role and completed an independently submitted job.

That command check is not a claim of actual native Linux/systemd service
activation: the available Ubuntu host still lacked installed Podman and
passwordless administration. No host changes were made after that permission
failure; validation-tool extraction stayed inside the ignored project cache.

The default Kind CNI is not evidence of enforced NetworkPolicy. A protected
production environment, network policy validation, migration-role separation,
deployment-wide role credential qualification, stateful HA and the declared
enterprise workload measurements remain release work.

## Container builds

`Containerfile` is the full source build. The default public npm endpoint failed
TLS negotiation in the current constrained build network. A follow-up build
using the operator's already configured HTTPS package mirror through the explicit
`NPM_REGISTRY` build argument passed, with lockfile integrity checks and TLS
verification enabled. No registry credential or personal registry URL is baked
into the Containerfile; its default remains the public registry.

The explicitly separate fallback `node scripts/package-image.mjs` builds the
static frontend and Linux Go binaries with the installed FOSS toolchain, records
a SHA-256 artifact manifest, and prepares `.artifacts/container`.
`docker build -f .artifacts/container/Containerfile -t aspm:0.1.0-m02
.artifacts/container` produced the first image used for the recorded Kubernetes test.
Podman can consume the same OCI build context. This is host-compiled artifact
assembly, not a signed-release or reproducibility certificate. The later full
source-image build is recorded separately as `aspm:0.1.0-source`.

When Docker's multi-platform image store prevents Kind loading an incomplete
image index, export only the selected platform and load the resulting archive.
Do not change host-wide Docker storage settings to fix this task's test cluster.

The later role-isolation lab image was assembled from the reviewed Linux core,
ingestion and reporting binaries plus the authenticated UI, using the shipped
runtime Containerfile. Its 93 backend source/control inputs remained unchanged
during both Windows and Linux builds. It deliberately excludes the unfinished
operational installer and is not a signed release bundle. The earlier full
source-image build does not establish release acceptance for the current tree.

Never package `.cache`, test secrets, local kubeconfig, npm credentials, private
keys or unrelated workspace files. The runtime image contains only application
binaries, built UI/notices, manifest and project notices.
