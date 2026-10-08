# M10 current coverage asset drill-down

Implemented on October 8, 2026.

This bounded M10 slice exposes the current asset membership behind the four
Live overview coverage counts. It adds no schema migration, queue, worker,
object-store object, provider call, or browser persistence. The migration
ledger for this slice remained versions 1 through 24; V25 is added separately
by current remediation SLA reporting.

## Read contract

`GET /api/v1/reports/coverage-assets` is available to every current member of
the selected workspace, including viewers. It requires:

- `state=scanned|unscanned|stale|unknown-freshness`
- `freshnessDays=1..365`
- Optional `limit=1..100`, defaulting to 100.
- Optional lower-case 32-hex asset `cursor`.

Duplicate values, empty values, unknown query keys, malformed numbers, invalid
states, and invalid cursors are rejected. Workspace authority comes only from
the authenticated selected membership, never a query or request body.

Each request captures server time once and uses one read-only repeatable-read
transaction. The freshness window is inclusive `[Now-freshnessDays, Now]`.
Results are ordered by asset ID and use cursor-exclusive manual pages. `total`
is the exact whole-filter count in that transaction.

## Coverage membership

Coverage uses the same source-scope calculation as Live overview. Qualifying
imports are succeeded, source-succeeded, complete, full scans grouped by asset,
source, scope ID, revision, and branch.

- A source timestamp after `Now` does not contribute. A future-only import does
  not establish coverage.
- `scanned` means at least one qualifying grouped scope exists.
- `unscanned` means no qualifying grouped scope exists.
- `stale` means at least one grouped scope's latest known source scan is older
  than the freshness-window start.
- `unknown-freshness` means at least one grouped scope has no known source scan
  time.

Scanned, stale, and unknown freshness intentionally overlap. For example, one
asset can have a fresh scope, a stale scope, and a scope with unknown source
time. `latestSourceScanAt` is the maximum known source scan across qualifying
scopes; it does not erase the independent stale or unknown flags.

Failed, source-failed, queued, partial, unknown-completeness, delta,
future-only, and other-workspace imports do not establish membership.

Every item contains the exact current asset DTO plus:

- `scanned`
- `stale`
- `unknownFreshness`
- `latestSourceScanAt`

The fixed verification limitation is:

`Coverage drill-down reflects successful complete full-scan intake; it is not verification of asset safety.`

## Reports UI

Only Live overview coverage values are interactive. Saved snapshot coverage
values remain plain text because a saved aggregate does not retain historical
asset membership.

Opening a Live coverage value performs one explicit 100-row read using the
freshness window shown by that overview. Editing the freshness draft does not
change drill-down requests until Refresh report applies it. Load more follows
the exact native cursor. Refresh coverage assets starts again from the first
page. Switching metrics replaces prior rows.

Transient failures preserve the last authorized page and retry context.
Forbidden or missing scope withholds prior rows until a matching authorized
response succeeds. Workspace changes, session replacement, logout, and
authentication rejection abort old reads and discard scoped data. No coverage
payload is stored in local storage, session storage, cookies, or URLs.

Separate pages are explicit reads, not one long-lived database snapshot. A
later page can reflect newer committed state; Refresh restarts from the current
first page.

## Qualification boundary

Real PostgreSQL acceptance covers exact four-state membership, overlapping
flags, inclusive freshness boundaries, grouped-scope latest time, future-only
exclusion, native paging, viewer authority, invalid queries, workspace
isolation, no database or object-store writes, unchanged snapshots and schema,
and one stable repeatable-read page while concurrent asset/import changes
commit.

Browser acceptance covers Live-only activation, applied freshness binding,
manual continuation and retry, metric replacement, authority loss and
recovery, strict malformed-response rejection, reduced motion, focus
preservation, and a 390-pixel viewport. This slice does not add historical
snapshot membership, finding drill-down, SLA tracking, or report exports.
