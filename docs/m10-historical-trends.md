# M10 snapshot-backed historical trends

Implemented on October 8, 2026.

This bounded M10 slice exposes historical posture trends from existing
immutable report snapshots. It adds no schema migration, queue, worker,
object-store object, provider call, or browser persistence. The migration
ledger remains versions 1 through 24.

## Read contract

`GET /api/v1/reports/trends?days=N` is available to every current member of the
selected workspace, including viewers. `days` defaults to 30 and must be one
decimal integer from 1 through 365. Duplicate values, unknown query keys, empty
values, and malformed values are rejected.

Each request captures server time once and uses one read-only repeatable-read
transaction. Its inclusive window is `[Now-N days, Now]`. Only succeeded
snapshots with a saved report and completion time inside that window are
eligible. Points are ordered by completion time and then snapshot ID.

The complete window is capped at 100 points. A 101st qualifying snapshot returns
`413 too-large`; the service does not truncate, page, sample, interpolate, or
silently narrow the window.

## Exact points and deltas

Each point contains the server-owned snapshot ID and name plus its saved
completion time, as-of time, totals, severity counts, and coverage counts. The
saved report is not recomputed against current findings. Its existing invariant
is preserved: the saved report as-of time equals the snapshot completion time.

With fewer than two points, `delta` is null. Otherwise it is the last point
minus the first point for:

- Findings and open findings.
- Accepted risk, suppression, and false positives.
- Critical, high, medium, low, and informational findings.
- Scanned, unscanned, stale, and unknown-freshness assets.

Negative, zero, and positive integer deltas are valid. There are no percentages,
rates, forecasts, SLA calculations, severity reclassification, missing-period
fill, or verification inference. Every response carries the fixed limitation:

`Historical snapshot trends are not an SLA, forecast, or independent verification.`

## Reports UI

The Historical trends panel is closed by default. Opening it performs one
30-day read. Editing Trend days does not fetch; Refresh trends explicitly
submits the current valid value. The panel shows exact window timestamps, point
count, saved snapshot rows, missing-period wording, and signed first-to-last
deltas.

Zero points is an authorized empty result. One point is shown without a delta.
Transient service failures retain the last authorized result with a diagnostic.
Forbidden or missing scope clears prior points and keeps them withheld until a
matching authorized response succeeds. Workspace changes, session replacement,
logout, and authentication rejection cancel old reads and discard scoped data.
Malformed successful responses are rejected without replacing the last valid
authorized result.

The client validates workspace authority, exact requested window, chronological
ordering, unique snapshot IDs, point bounds and timestamps, metric shapes and
consistency, exact delta arithmetic, verification wording, API version, and
live data origin. Trend data is not written to local storage, session storage,
cookies, or URLs.

## Qualification boundary

Real PostgreSQL acceptance covers inclusive membership, tied completion
ordering, exact saved values and deltas, viewer access, invalid queries,
workspace isolation, the 100/101 boundary, no database or object-store writes,
unchanged saved snapshot bytes, unchanged schema versions, and one stable
repeatable-read snapshot while a later row is committed concurrently.

Browser acceptance covers explicit reads without polling, empty and one-point
states, timestamp gaps, authority loss and recovery, strict malformed-response
rejection, focus preservation, reduced motion, and a 390-pixel viewport. This
slice does not add SLA tracking, drilldown, forecasting, scheduled reports, or
report export generation.
