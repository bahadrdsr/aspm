# M06 retention execution

Implemented on October 7, 2026. This increment applies an exact approved
retention preview through an independent, fenced retention worker. It preserves
finding identity, Work membership, notes, decisions, source state, observations
and correlation lineage.

## Independent worker and storage authority

`cmd\retention-worker` owns one database pool and one explicit retention S3
credential. It has no Core API, authentication, provider or browser surface.
The selected credential can read/write only the configured raw and archive
prefixes. Core keeps its existing raw intake authority and receives read-only
archive access for authorized retrieval. Ingestion has no archive access and
Reports remains database-only.

Helm, Quadlet, the installer credential material and image packaging include
the retention role. Existing installer material can add the retention identity
only during an explicitly approved reapply whose plan is bound to that
identity; later runs must match it exactly.

## Durable operations

An administrator queues execution only from an approved preview using its exact
revision and SHA-256 snapshot digest, plus a rationale and idempotency key.
Changed replay conflicts. The API request creates a durable run and item
manifest but performs no object I/O.

The worker claims one item per call with a database lease and fence. Successful
items reset the run ownership counter. Repeated abandoned ownership is bounded,
and another worker can resume queued or expired work. Every final database
mutation checks the live run fence.

The supported actions are:

- `archive-history`: write exact normalized observation JSON to the archive
  prefix, verify it, retain a compact database summary, then mark the
  observation `archived`.
- `expire-raw-report`: verify the immutable raw report, record an `expiring`
  transition, delete the selected raw object, then mark it `expired`.
- `expire-archive`: record an `expiring` transition, delete an archived
  observation object, then retain its summary and an `expired` tombstone.
- `archive-audit`: archive exact correlation before/after detail while retaining
  actor, rationale, sequence and event lineage in PostgreSQL.
- `restore-archive`: verify an archived observation, restore its exact JSON to
  hot PostgreSQL history, and mark it `available`.

Archived-evidence age starts at the verified archive time, not the original
scan/import time. Later holds, decisions and assessment references are included
in new previews and rechecked again by execution.

The transition marker makes deletion resumable. A crash before deletion retries
the exact object; a crash after S3 deletion but before finalization can safely
finish the policy-authorized expiry without relabeling it as accidental
missing evidence.

## Protection rechecks and outcomes

Execution rechecks current holds, active decisions, notes, active correlation,
assessment references and shared raw-report references immediately before
mutation. A protection added after approval produces a durable `protected`
item outcome. It does not bypass the new state because the preview was once
approved.

Runs expose item states `queued`, `processing`, `succeeded`, `protected`,
`missing`, `corrupt` and `failed`. A run is `partial` when an expected object is
missing/corrupt or an item exhausts its attempts. Protected items are an
expected safe outcome and do not by themselves make the run partial.

## Evidence availability and retrieval

Imports and observations expose:

- `available`: hot bytes are readable.
- `archived`: normalized observation bytes are retrievable through the current
  workspace authorization boundary.
- `expired`: an approved retention operation removed the selected bytes.
- `missing`: the referenced object is unexpectedly absent.
- `corrupt`: size or SHA-256 verification failed.

Raw-report evidence returns a specific expired/missing/corrupt API error rather
than a generic absent response. Archived observation retrieval verifies exact
bytes. A missing or corrupt archive read persists that observed state. Only a
workspace administrator can queue restoration; completion still requires the
independent worker and a manual status refresh.

## Verification completed

- Real PostgreSQL/S3 acceptance covers approved execution, exact replay,
  post-approval hold/decision rechecks, shared-reference protection,
  one-item processing, close/reopen resume, bounded abandoned ownership,
  lease renewal across a held storage operation, cancellation/reclaim, raw
  expiry, normalized archival, interrupted raw/archive delete finalization,
  missing/corrupt controls, foreign-workspace denial, audit archival and exact
  restoration.
- The authentic pinned published V11 closure migrates through V15 while
  preserving historical API/native data and exact old business rows.
- Installer tests cover the additive retention credential, secret, Helm values,
  Quadlet unit, service activation and explicit reapply binding.
- A live SeaweedFS 4.47 policy fixture verifies role-signed read/write/delete
  behavior for selected raw/archive prefixes and denial outside them.
- Browser acceptance covers explicit execution queue/refresh plus archived
  observation retrieval and restoration.

## Remaining limits

- Preview remains whole-workspace and capped at 200 resources.
- Restoration currently covers normalized observations, not expired raw reports
  or archived correlation detail.
- Restored observations retain their verified archive copy.
- Policy-authorized deletion can leave an unreferenced archive object if the
  database finalization loses its fence after an archive write; orphan garbage
  collection remains separate.
- Recurring-scan storage/index/WAL growth and restore-duration budgets are not
  yet measured.
- Linux activation and a supported release still require their broader M13
  qualification.

The next M06 work is bounded candidate matching and multi-member correlation,
change-focused lifecycle classification, and recurring-scan churn/capacity
qualification.
