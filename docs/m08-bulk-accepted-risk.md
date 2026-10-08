# M08 bulk accepted risk

Implemented on October 8, 2026.

V25 extends the existing selected-findings bulk endpoint with one bounded human
decision: accepted risk. It does not add bulk suppression, false-positive,
clear-disposition, AI, proof, or automatic expiry behavior.

## Reviewed selected set

Administrators and analysts can submit 1 through 100 distinct visible findings
with:

- The exact current decision revision reviewed for every finding.
- Disposition `accepted-risk`.
- An explicit null or future RFC3339 expiry.
- One bounded nonblank rationale.

Owner and workflow fields cannot be mixed with risk fields. Existing owner and
workflow bulk requests retain their previous request shape.

The service sorts finding IDs, locks them deterministically in one transaction,
and compares every current decision revision before changing any row. A stale,
foreign, hidden correlation member, deleted, missing, malformed, duplicate,
over-limit, or unauthorized selection changes nothing.

## Independent immutable history

Every successful finding:

- Preserves owner, workflow, source/evidence state, notes, observations,
  correlation membership, and verification state.
- Increments its decision revision once.
- Receives its own finding-scoped immutable accepted-risk approval.
- Receives its own `bulk-update` decision event with exact before/after state.

Prior accepted-risk, suppression, and false-positive approvals remain immutable.
A changed rationale or expiry is a new reviewed decision. An exact no-change
request returns conflict and creates no empty history.

The canonical Work item now includes decision revision, disposition,
accepted-risk expiry, and computed expiry state. Work and Reports reflect the
committed accepted-risk totals immediately. New scans preserve the human
decision and do not create or remove approvals or decision events.

## UI

The selected-findings toolbar includes **Accept risk**, optional expiry, and a
required rationale. It explains that one immutable finding-scoped approval is
created per finding and that acceptance does not verify safety.

Successful server acknowledgements update the selected rows and counts, clear
the completed selection, and retain Work context. Stale, denied, unavailable,
workspace, session, and logout boundaries retain or clear drafts according to
current authority without accepting late acknowledgements.

V25 adds no schema migration. The database remains at version 24 and new
decision events use the existing V23/V24 preview, archive, and retrieval path.
