# M10 bounded report exports

Implemented on October 9, 2026.

V26 adds bounded asynchronous JSON and CSV exports for succeeded immutable
report snapshots. Export intent, worker state, exact artifact bytes, digest,
and size remain in PostgreSQL. The report worker stays database-only and does
not receive S3 or provider credentials.

## Durable contract

`app_report_exports` stores one workspace-scoped export intent and artifact.
The format is `json` or `csv`; state is queued, processing, succeeded, or
failed. Artifacts are limited to 256 KiB.

Only succeeded rows contain content, a lower-case SHA-256 digest, exact byte
size, and completion time. Failed rows contain a bounded terminal diagnostic
and no artifact. Queued and processing rows expose no completion, failure, or
artifact metadata.

Admins and analysts create exports with:

```text
POST /api/v1/reports/exports
{snapshotId,format,idempotencyKey}
```

The snapshot must already be succeeded in the selected workspace. The first
accepted request returns 202 and commits only a queued job. Exact
workspace-scoped replays return the original receipt and requester. Reusing a
key with a different snapshot or format conflicts.

Every current workspace member can use:

- `GET /api/v1/reports/exports`
- `GET /api/v1/reports/exports/{id}`
- `GET /api/v1/reports/exports/{id}/content`

History uses an ascending native ID cursor, a maximum page size of 100, and one
read-only repeatable-read transaction for total and rows. Content is available
only after success.

Before returning bytes, the service verifies the stored size and SHA-256
digest. Corruption fails closed with `evidence-corrupt` before artifact headers
or bytes are written.

## Worker behavior

`ProcessReports` drains report snapshots and report exports. It alternates
available work so neither queue can starve, while retaining the existing
90-second database lease, 60-second work deadline, three attempts, monotonic
fence, expired-lease reclaim, and cancellation requeue behavior.

An export reads only the already-succeeded snapshot row and its saved typed
report. It does not recompute posture or rewrite the snapshot. Exact bytes,
digest, size, completion, and succeeded state publish in one transaction under
the current worker and fence. Cancellation, an expired lease, or a stale fence
cannot publish.

After three generation failures, the job becomes failed with
`report-export-generation-failed`. No object-store or provider request is part
of export processing.

## Canonical artifacts

JSON uses stable struct field order and one trailing LF:

```text
{"apiVersion":"aspm/v1alpha1","export":{"snapshotId":...,"name":...,"completedAt":...,"report":...}}
```

CSV uses UTF-8, exact CRLF records, and the fixed
`section,metric,value` layout. It includes snapshot identity and completion,
report workspace and as-of time, all saved totals, severity counts, coverage,
freshness window, and verification state and reason.

Snapshot names receive an apostrophe prefix when needed to prevent spreadsheet
formula interpretation. Other saved strings remain literal CSV values.

## Reports UI

Report exports is closed by default and performs no request while closed.
Opening it reads only the first 100-row export page. Refresh and Load more are
explicit; there is no polling or automatic page drain.

Admins and analysts can create an export from succeeded snapshots already
loaded by Saved snapshots. The browser does not fetch or drain extra snapshot
pages for the selector. Viewers retain history, detail, refresh, and download
access without a create form.

Queued and processing receipts have no Download action. Failed receipts show
the terminal diagnostic. Succeeded receipts show exact digest, size, and safe
filename.

Download reads the content endpoint into bounded memory, validates content
type, disposition, digest, declared size, actual size, and SHA-256, then creates
one temporary object URL and revokes it. Artifact bytes, metadata, intent keys,
and object URLs are not stored in browser storage.

An ambiguous create acknowledgement retains one unresolved in-memory intent.
The browser does not invent another key or retry automatically. Explicit
Refresh exports can reconcile a newly visible matching receipt.

## Qualification boundary

Real PostgreSQL acceptance covers exact V26 migration and V25 projection,
authority, idempotency, native paging, repeatable-read stability, exact JSON and
CSV bytes, digest and size checks, snapshot immutability, cancellation, lease
reclaim, stale fencing, attempt limits, corruption detection, reopen
persistence, and draining both report queues.

Browser acceptance covers lazy loading, create and unresolved acknowledgement
flows, explicit state refresh, native continuation and retry, viewer access,
workspace and session boundaries, strict response and artifact validation,
focus preservation, reduced motion, and a 390-pixel viewport.

Exports reproduce saved report data. They do not run scans, verify safety,
reconstruct historical membership, or prove remediation.
