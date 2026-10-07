# M06 change-focused lifecycle

Implemented on October 7, 2026. V17 classifies each current source projection
and every positive observation without changing human workflow, disposition or
independent verification.

## Finding lifecycle

Each finding exposes:

- `new`: the first successful admitted observation for this source identity.
- `changed`: a newer successful observation changed normalized source content.
- `unchanged`: a newer successful observation retained the same semantic
  content, or an upgraded historical finding established its first digest.
- `reopened`: a newer positive observation followed scanner-inferred
  resolution.
- `inferred-resolved`: a newer successful complete full scan in the exact same
  source scope did not contain the finding.

The semantic digest covers normalized source identity/content, severity,
location, evidence, remediation, impact and unmapped source fields. It excludes
run ID, scan ID, collection/import time and report digest, so repeating the same
finding does not become a change merely because another scan ran.

`change_revision` advances only when a successful current source projection or
comparable absence becomes current. `change_run_id` binds that projection to
the exact run without relying on equal timestamps. Historical V11-V16 rows
migrate with `change_kind=unchanged`, no change timestamp/run and an empty
digest; the next successful observation establishes the digest without
refreshing unrelated fields.

## Observation classification

Every new observation records its own `changeKind` plus explicit reasons:

- `historical` with `out-of-order` when an older observation is retained but
  does not replace the current source projection.
- `non-authoritative` with `source-status-failed` when the scanner-declared run
  failed.
- `partial-scan`, `unknown-completeness`, `delta-scan` and
  `unknown-source-time` remain visible context on positive observations.

Failed, partial, delta, stale, out-of-order and changed-scope inputs therefore
remain in provenance without gaining authority they do not have. Changed scope
creates a separate source-specific finding instead of closing or rewriting the
existing one.

## Meaningful Work

`GET /api/v1/work?change=meaningful` uses the V17 partial index and returns only
current `new`, `changed` and `reopened` findings. Default Work still returns all
visible findings. The response echoes `changeMode` so the client cannot mistake
an unfiltered response for the meaningful queue.

The Work UI:

- Shows the service-owned source change on every row.
- Provides an explicit **Meaningful changes only** toggle.
- Keeps last confirmed rows visible while the mode refresh is pending.
- Preserves selection and deliberately moved keyboard focus.
- Does not create a new analyst task for an unchanged observation.

The complete 151-case browser suite passes with the meaningful-change refresh,
selection and focus workflow.

Finding detail shows current change revision and scanner/source state
separately. Observation history shows classification and reason context.
Human workflow, ownership, notes, disposition and verification remain
independent.

## Qualification

Real PostgreSQL/S3 acceptance covers:

- New, unchanged, changed, inferred-resolved and reopened transitions.
- Historical out-of-order observations.
- Partial and delta context.
- Failed-scan non-authoritative observations.
- Changed-scope isolation.
- Preserved owner, workflow and notes.
- Meaningful Work membership after every transition.

The recurring-scan gate imports 25 successful complete full scans of one
unchanged finding and verifies:

- One finding row.
- 25 observation rows.
- Lifecycle revision 25.
- No meaningful Work after the final unchanged scan.
- One immutable raw object per scan.
- 57,344 bytes of measured finding/observation relation growth.
- Zero measured candidate-index and meaningful-change-index growth.
- 213,584 bytes of measured PostgreSQL WAL growth.

The enforced engineering bounds are 8 MiB table growth, 1 MiB per selected
index and 32 MiB WAL for this owned fixture. These are regression bounds, not
enterprise capacity claims.

## Remaining limits

- No automatic/fuzzy correlation.
- No orphan archive-object garbage collection.
- No enterprise duration/throughput/partition benchmark.
- Analysis conclusion and proof outcome remain separate advisory/verification
  modules rather than Work lifecycle states.
