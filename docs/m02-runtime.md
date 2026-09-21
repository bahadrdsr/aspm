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

Delivery packaging is opt-in. `delivery.enabled` is a strict boolean and defaults
to `false`, leaving the original three application roles unchanged. An explicit
`integrationKeySecret.name` and `.key` select an existing integration-encryption
Secret key for core, including when the worker is disabled. Partial selections
are rejected. Enabling delivery requires both selectors and gives only core and
delivery that required reference; ingestion and reports never receive it.
The delivery Deployment has its own replicas/resources, runs
`/app/bin/delivery-worker`, and receives database configuration, the selected
integration key, `delivery.leaseDuration` (default `"15s"`) and
`delivery.slackEndpoint` (default `"https://slack.com"`). It receives no storage,
bootstrap or UI credentials. The chart rejects non-string lease/gateway values
and credential-bearing gateways; the runtime remains authoritative for full
duration and HTTPS-base validation.

Source collection is a separate opt-in. `collection.enabled` defaults to the
boolean `false`; neither it nor `delivery.enabled` enables the other role.
`collection.storage` selects explicit `endpoint`, `bucket`, `prefix` and `region`
strings, without managed/raw storage defaults. `core.collectionS3Secret` selects
the reader's `name`, `accessKeyKey` and `secretKeyKey`. Complete storage and
reader settings can preconfigure core while collection remains disabled.
Any partial storage or selector configuration is rejected even while disabled.

Enabling collection also requires a complete `collection.s3Secret` publisher
selection and the existing `integrationKeySecret`. Core and collection get the
same four collection-storage values but their own selected key references.
Identical reader/publisher reference tuples are rejected; one Secret with
distinct reader/publisher key names is allowed. Different references alone do
not prove distinct credential bytes or permissions. The separate real
storage-key gate establishes publisher PUT, core-reader GET and reader PUT
denial for its explicitly selected owned fixture.

The independent collection Deployment runs `/app/bin/collection-worker` with
its own replicas/resources, DB settings, integration key, publisher storage,
`collection.leaseDuration` (default `"15s"`) and `collection.githubEndpoint`
(default `"https://api.github.com"`). Ingestion, reports and delivery receive
none of the collection settings or reader/publisher keys; legacy raw storage
and managed-service policy remain unchanged. The chart rejects non-string or
blank fields, credential-bearing endpoints and non-HTTPS provider gateways.

Assessment is independently opt-in with `assessment.enabled: false` and an
empty `assessment.scope` by default. For an explicitly selected shared admission
scope, the minimal worker override is:

```yaml
assessment:
  enabled: true
  scope: "reviewed-team/assessment"
```

Enabling requires a nonempty string scope. A nonempty scope is also forwarded
unchanged to core while `assessment.enabled: false`, allowing core-only
preconfiguration without starting a worker. Empty/off adds no assessment
authority. Scopes accept Unicode and slashes, are limited to 128 UTF-8 bytes,
and cannot have surrounding whitespace or control characters. Malformed
supplied scopes fail rendering even while disabled; flags must be booleans.

The assessment Deployment runs only `/app/bin/assessment-worker`, independently
of delivery and collection; all three opt-ins can coexist. It defaults to one
replica, resource requests of `100m` CPU/`128Mi` memory and limits of `1` CPU/
`512Mi` memory. `assessment.replicas` and `assessment.resources` control only
that Deployment. It retains nonroot UID/GID 10001, a read-only root filesystem,
no privilege escalation, all capabilities dropped, RuntimeDefault seccomp and
no service-account token. Its sole writable mount is a `256Mi` memory-backed
`/tmp`. Startup/readiness use `/readyz` and liveness uses `/healthz` on port 8080.

Assessment receives the existing database Secret's required `database-url`
reference, selected schema/pool settings, listen address, scope and these exact
worker settings, rendered as string environment values:

| `assessment` value | `ASPM_ASSESSMENT_` suffix | Default |
| --- | --- | --- |
| `leaseDuration` | `LEASE_DURATION` | `"15s"` |
| `authorizationInterval` | `AUTHORIZATION_INTERVAL` | `"100ms"` |
| `requestTimeout` | `REQUEST_TIMEOUT` | `"10s"` |
| `requestWindow` | `REQUEST_WINDOW` | `"1m"` |
| `maxConcurrent` | `MAX_CONCURRENT` | `1` |
| `requestsPerWindow` | `REQUESTS_PER_WINDOW` | `30` |
| `maxInputBytes` | `MAX_INPUT_BYTES` | `32768` |
| `maxOutputTokens` | `MAX_OUTPUT_TOKENS` | `1024` |
| `maxResponseBytes` | `MAX_RESPONSE_BYTES` | `65536` |

Durations must be nonblank strings. Numeric settings must be positive integer
numbers, not strings or fractions, with maxima of 16, 1000, 32768, 32768 and
131072 respectively. Invalid supplied values never restore defaults.
The runtime remains authoritative for full duration grammar, cross-field
constraints and execution-time admission. Replicas sharing a scope must use
matching admission settings; adding replicas does not grant independent quota.
Core receives only the scope, not worker limits or client settings. Ingestion,
reports, delivery and collection receive no assessment-specific environment.

Assessment may use the existing complete `integrationKeySecret.name`/`.key`
selection, shared with core through required, nonoptional Secret references.
Omission or the default `{}` is valid for keyless local profiles; no encryption
key environment value is then emitted. Partial selectors fail even when the
worker is disabled. Delivery/collection still require their existing key
configuration when enabled. The assessment role receives no storage, AWS,
bootstrap, assets or readiness-preparation credentials, and adds no Secret,
Service, service account, RBAC or ingress authority.

This first chart profile uses ordinary system TLS trust. A private/local TLS CA
requires EXPLICIT MANUAL operator provisioning through the already supported
`ASPM_ASSESSMENT_CA_FILE` runtime input, including a readable certificate-only
file in the actual worker environment. The chart does not provision a CA file,
path setting, Secret mount or generic environment extension. It does not disable
TLS verification. Persisted reviewed AI profiles, endpoints, keys, policies and
grants remain application configuration, not chart model-routing knobs.
Readiness reports configured admission and database availability, not model
access, available quota, provider-key validity or processing geography.
Selecting a local profile does not itself certify local inference.

On September 21, 2026, the owned two-node Kubernetes lab explicitly enabled one
assessment replica using the actual source-built seven-command image. Core and
assessment received the same selected scope; ingestion, reports, delivery and
collection received no assessment-specific settings. The existing database,
integration-key and storage selections were unchanged. All six application roles
became ready, with two ingestion replicas and one of each other role.

The running assessment executable matched the independently checked source-image
hash. Its actual `/readyz` reported database reachability, `storage:not-required`,
the selected admission scope and `provider:not-probed`; three application/auth
routes returned HTTP 404. No provider job was submitted in this cluster check.
The inherited image-level assets default is not a worker capability: the role
parser still opens no static/auth handler or raw storage client.

This is owned-lab rollout and startup evidence, not native systemd activation,
private-CA or keyless-inference provisioning, available quota, live-model access
or production NetworkPolicy enforcement. A private PostgreSQL dump was retained
before the additive upgrade; that is not a coordinated object-store backup or
tested restore claim.

On September 17, 2026, the owned Kind lab enabled collection using the full
source-built six-command image. Two new identities were manually provisioned
for `collections/`: a core reader and a collection publisher. Existing storage
identities and database/storage volumes were retained. The deployed environment
values matched those selected keys, and ingestion, reporting and delivery did
not receive collection credentials.

Direct calls to the deployed S3 service proved publisher PUT and exact reader
GET, plus seven native HTTP 403 denials: reader overwrite/delete, publisher
outside-prefix write, both outside-prefix reads and both whole-bucket probes.
Denied operations preserved existing bytes. The worker reported database
readiness with `storage:configured-not-probed`; the permission proof was a
separate operation, not inferred from that readiness label. The lab used the
reserved `https://github.invalid` endpoint and did not run a live GitHub
collection. Existing HTTPS login, intake and report workflows remained functional.
Full URL/storage/duration validation remains the runtime's responsibility.
No new Secret, policy, storage-rights grant or ingress is generated.

On September 17, 2026, the owned Kubernetes lab explicitly enabled delivery
with a separate integration-key Secret. Core and delivery read identical selected
key bytes; ingestion and reporting had no integration key, and delivery had no
storage/bootstrap credentials. The actual packaged worker became ready with
database access and `storage:not-required`. A reserved `https://slack.invalid`
gateway was selected, so this rollout qualifies role startup and key wiring,
not a live Slack send. The existing HTTPS authentication, intake and report
flows remained functional. Actual native delivery was separately exercised
against the owned certificate-validated protocol fixture.

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

The separate `aspm-delivery.container` uses only the protected
`/etc/aspm/delivery.env`, a read-only filesystem and the existing private network.
It has no storage-service dependency or `[Install]` activation directives.
The installer still copies and starts only its existing roles; it does not
provision this env file or activate delivery. Protected key/file provisioning
and an explicit operator-controlled worker start are separate from shipping
the unit. Client rendering and rootless native-generator dryruns are not
Kubernetes/systemd activation or proof that credentials have been provisioned.

`aspm-collection.container` likewise remains manual opt-in with no `[Install]`
configuration. Its only environment source is `/etc/aspm/collection.env`, which
must be provisioned separately with the explicit publisher identity and other
runtime inputs. The unit retains the read-only filesystem, bounded temporary
filesystem and private network, without inline credentials or extra volumes.
Existing installer bundle/copy/start behavior is unchanged and does not install,
provision or start this new unit. Rootless Podman 4.9 generation verifies syntax
and generated command text, not an existing protected file or service activation.

`aspm-assessment.container` is likewise a manual opt-in, with no `[Install]`,
autostart or alias directives. It runs the actual assessment command on the
existing `aspm.network`, using only `/etc/aspm/assessment.env`, a read-only
filesystem, no-new-privileges, dropped capabilities and bounded `256m` `/tmp`.
There is no inline environment, additional credential volume or storage-service
dependency. The operator must separately provision the protected file with
database settings, the same explicit scope as core, selected limits and, only
when needed, the matching integration-encryption key. For a keyless profile,
omit that key variable rather than supplying an empty value. Private CA
provisioning remains manual as described above. The existing Linux installer
and bundle lists are unchanged: they neither copy/start assessment nor create
its env file. Native unprivileged Podman 4.9 dryrun output is syntax evidence
only; no generated service command is executed by artifact validation.

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

The source and host-build command inventories include `assessment-worker`,
`collection-worker` and `delivery-worker` alongside core, ingestion, reporting
and `aspmctl`, for seven actual command targets. Both image
recipes retain the complete
binary directory and default to `/app/bin/core-api`; adding a worker does not
change the default process. These recipe changes alone do not prove that a
current image contains the worker. Actual image construction and executable
checks remain separate from artifact source/render/native-generator validation.

The September 21 assessment qualification built the full source Containerfile as
`aspm:0.1.0-assessment-optin-source` and ran the actual host packager to build
`aspm:0.1.0-assessment-optin-host`. Both images contained all seven executable
commands and the static UI, retained UID/GID 10001 and the core default command,
and passed network-disabled, read-only inventory checks. The host-image binary
hashes matched the actual packager manifest. Linux/amd64 was exercised; no
cross-architecture claim is made.

The source build initially failed public-registry TLS negotiation. Its successful
run selected the previously used HTTPS package mirror through `NPM_REGISTRY`,
with normal certificate validation and the lockfile unchanged. The default
registry and repository trust settings were not changed. The source-built image
was imported into the owned lab as an explicit amd64 archive and used by the
rollout described above. No image was published to a remote registry or claimed
as a signed supported release.

The earlier September 17 delivery packaging check built the source Containerfile,
verified all five executable binaries and the non-root core default, and ran the
delivery binary with networking disabled to confirm missing-key preflight denial.
The actual host packaging helper also produced five ELF binaries whose hashes
matched its generated manifest. These development checks are not a signed
release, cross-architecture certification or systemd activation. They do not
establish that a later image includes the newly added sixth collection command;
that requires a separate current image-build and binary check.

The later `aspm:0.1.0-collection-optin` source build separately verified all six
executables, the unchanged non-root core default, and a network-disabled
collection binary rejecting missing configuration. The actual host packaging
helper also produced six ELF binaries with matching manifest hashes. The
source-built image was used for the owned collection rollout described above.
This does not certify another architecture, a signed release or native systemd
activation.

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
