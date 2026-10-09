# M10 current finding metric drill-down

Implemented on October 9, 2026.

This bounded M10 slice exposes the current visible canonical finding membership
behind Live overview counts. It adds no schema migration, queue, worker,
provider call, object-store object, or browser persistence.

## Canonical current totals

Live overview and new report snapshots now use the same visibility rule as
Work: an active correlation secondary is hidden and is not counted as another
canonical finding. Releasing or splitting the member makes it visible again.

Existing saved snapshot JSON is immutable and is never recomputed. New
snapshots store the corrected visible-canonical totals.

## Read contract

`GET /api/v1/reports/finding-metrics` accepts one exact metric:

- Findings and open findings.
- Accepted risk and expired accepted risk.
- Suppressed and expired suppression.
- False positive.
- Source-inferred resolved.
- Critical, high, medium, low, or informational severity.

The endpoint also accepts optional `limit=1..100` and an exclusive lower-case
32-hex finding cursor. Every current workspace member, including viewers, can
read. Workspace authority comes only from the selected authenticated
membership.

One page captures server time once and uses one read-only repeatable-read
transaction for total and rows. Items are ordered by finding ID. There is no
offset, automatic page drain, write, worker, provider, or object-store access.

The browser binds reads to the exact displayed Live overview as-of time through
a scoped request header. This keeps accepted-risk and suppression expiry
membership aligned with the displayed aggregate. A response whose whole total
no longer matches the overview is rejected and requires an explicit Live
overview refresh.

## Membership

All metrics use visible canonical findings:

- `findings`: every visible finding.
- `open-findings`: human workflow is not resolved.
- `accepted-risk`: current disposition is accepted risk.
- `expired-accepted-risk`: accepted-risk expiry is at or before as-of.
- `suppressed`: current disposition is suppressed.
- `expired-suppression`: latest matching immutable suppression approval expiry
  is at or before as-of.
- `false-positive`: current disposition is false positive.
- `inferred-resolved`: current source state is inferred resolved.
- Severity metrics: exact current severity, independent of workflow and
  disposition.

A finding can belong to several metrics at once. Human workflow, disposition,
source state, and severity remain independent dimensions.

Each row contains current finding/asset identity, severity, owner, workflow,
disposition, accepted-risk expiry and computed expiry state, source state, and
source freshness.

The fixed limitation is:

`Finding metric drill-down reflects current canonical finding state; it does not verify safety or historical membership.`

## Reports UI

Only Live overview finding metrics are interactive. Assets and Verified
resolved remain plain values. Saved snapshot metrics remain noninteractive
because snapshots store aggregates, not historical member lists.

Opening a metric performs one explicit 100-row read. Load more follows the
native cursor. Refresh finding membership starts from the first page. Switching
metrics immediately clears old rows. There is no polling.

Transient failures preserve the last authorized rows and retry cursor.
Permission or missing-scope denials withhold rows until an authorized response
succeeds. Workspace changes, session replacement, logout, and authentication
rejection abort old reads and clear scoped data. No row or request context is
stored in browser storage.

## Qualification boundary

Real PostgreSQL acceptance covers exact overview parity, hidden active
correlation members, split release, all metric overlaps, expiry boundaries,
latest suppression approval semantics, viewer and workspace authority, native
paging, as-of binding, unchanged old snapshot bytes, corrected new snapshots,
no side effects, and repeatable-read stability during concurrent changes.

Browser acceptance covers Live-only activations, exact current context, native
continuation/retry, metric replacement, explicit Live refresh after drift,
strict malformed-response rejection, authority loss and recovery, focus
preservation, reduced motion, and a 390-pixel viewport.

Historical member lists, scheduled reports, and bounded report exports remain
separate work.
