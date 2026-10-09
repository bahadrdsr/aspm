# M10 current remediation SLA

Implemented on October 8, 2026.

V25 adds current remediation time targets without changing source truth, human
decisions, AI advice, or verification. SLA is a time-to-human-workflow view,
not a safety result, legal commitment, or historical reconstruction.

## Stable age and migration

Each finding now stores `first_observed_at`. Existing rows are backfilled from
the earliest import reachable through their immutable observations, with the
current finding import time used only when no observation/import remains.

New findings use the first accepted import time. Reimports, changed or reopened
observations, source timestamps, decisions, and rescans never move this anchor.
Split findings retain their own stored anchors.

For a visible primary in an active correlation, SLA reads use the minimum
stored first-observed time across active members. Released members stop
contributing. This effective time is computed during reads and is never written
back into finding history.

## Versioned workspace policy

Every workspace has one current policy and immutable complete policy revisions.
Initial targets are:

- Critical: 7 days.
- High: 30 days.
- Medium: 90 days.
- Low: 180 days.
- Informational: 365 days.

These are starter values, not a standard. Administrators can explicitly replace
all five targets using the exact current revision and a bounded rationale.
Targets must be integers from 1 through 3650 and remain ordered from critical
through informational. Stale updates and exact no-ops conflict. Analysts and
viewers have read-only access.

## Current SLA calculation

`GET /api/v1/reports/sla` captures server time once and uses one read-only
repeatable-read transaction. It tracks visible canonical findings whose human
workflow is open, in progress, or pending retest. Human workflow `resolved` is
the only state that removes a finding from current tracking.

Accepted risk, suppression, false positive, disposition expiry, source state,
source-inferred resolution, AI output, and verification do not pause, satisfy,
or verify the SLA clock.

For current severity:

```text
dueAt = effectiveFirstObservedAt + targetDays
```

`dueAt` equal to server time remains within target. A finding is breached only
when `dueAt` is earlier than server time. Overdue seconds are whole elapsed
seconds after the deadline. There are no business calendars, time-zone pauses,
owner pauses, forecasts, or historical policy reconstruction in this slice.

The fixed limitation is:

`Remediation SLA status is a time-to-workflow target; it does not verify safety, resolution, or risk acceptance.`

## Current finding pages

`GET /api/v1/reports/sla-findings` accepts `breached` or `within-target`, a
bounded native limit, and an exclusive finding cursor. Pages are ordered by
visible canonical finding ID. Each row contains current asset, severity, owner,
workflow, disposition, source state, stable first-observed time, due time,
target, status, and overdue seconds.

The browser binds list reads to the server-issued summary time and policy
revision. A stale policy revision conflicts instead of mixing calculations.
Current finding membership can still change between explicit reads; Refresh SLA
and Refresh SLA findings request current state again.

## Reports UI

Remediation SLA is closed by default and never polls. Any workspace member can
open the summary and explicit breached/within-target finding pages. Pages use
manual Load more and explicit refresh.

Current administrators can open Edit SLA targets, review the current revision,
enter all five targets plus rationale, and save explicitly. There is no
optimistic success. A canonical acknowledgement updates the displayed policy,
then the summary and any open first finding page are refreshed.

Transient failures preserve the last authorized data. Permission or missing
scope denials withhold the affected data until an authorized response succeeds.
Workspace changes, session replacement, logout, and authentication rejection
abort old reads and clear protected data and drafts. No SLA policy, draft,
summary, or finding row is stored in browser storage.

## Qualification boundary

Real PostgreSQL acceptance covers exact V25 catalog projection to V24,
observation-derived backfill, immutable policy revisions, new-workspace
defaults, stable rescans, correlation/release behavior, exact due boundaries,
all workflow/disposition/source controls, authority, paging, no side effects,
and repeatable-read stability during concurrent changes.

Browser acceptance covers lazy reads, native pages, explicit policy editing,
conflicts and denials, strict malformed-response rejection, focus preservation,
reduced motion, and a 390-pixel viewport.

Scheduled reports, business calendars, contractual SLA management, historical
SLA reconstruction, and report exports remain separate work. Current Live
finding metric drill-down is documented separately.
