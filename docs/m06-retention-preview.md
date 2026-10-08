# M06 retention and archive preview

Implemented on October 7, 2026. This is the policy, hold, preview and approval
increment. Execution is documented separately in `m06-retention-execution.md`.

## Policy classes

Every workspace has an additive V14 policy row with four independent positive
day bounds:

- Hot history: 90 days by default, proposed action `archive-history`.
- Raw reports: 180 days by default, proposed action `expire-raw-report`.
- Archived evidence: 365 days by default, proposed action `expire-archive`.
- Audit: 730 days by default, proposed action `archive-audit`.

Administrators can update all four values using the current policy revision.
The values must stay strictly ordered from hot history through audit and cannot
exceed 3650 days. These defaults are product hypotheses, not legal advice.

## Holds and protected references

Administrators can create a durable hold for one import, observation or
correlation audit event. A hold records its creator, reason, revision and
creation time. Release requires the current revision and a rationale; released
holds remain visible as history. An observation hold also protects its
underlying raw report object.

Preview protects resources for these current reasons:

- An active legal hold.
- A linked finding with an owner, non-default workflow/disposition, note or
  active correlation.
- Multiple observations sharing one raw report.
- An assessment preview referencing an observation.
- An audit event belonging to an active correlation.

The protected reason is returned for each resource instead of silently removing
it from counts. Observation assessment references also protect the underlying
raw report candidate.

## Exact preview and approval

Any workspace member can request a preview. It is capped at 200 resources and
expires after 15 minutes. The response separates all four policy classes and
includes:

- Proposed action and retention days.
- Total, eligible and protected counts.
- Eligible logical payload bytes. This is not a filesystem, index, WAL or object
  storage savings measurement.
- Exact resource kind, ID, observed time, size and protection reasons.
- Policy revision and a SHA-256 snapshot binding.

Creating a preview writes only the preview receipt and its item manifest. It
does not move or delete objects, compact history, update findings, change source
state or alter Work membership.

V14 adds workspace/age and reference indexes for the bounded import,
observation, assessment and correlation-event candidate reads. This is not an
enterprise capacity claim; previews still fail closed above the 200-resource
bound.

Only an administrator can approve a preview. Approval requires the exact
preview revision/digest, a rationale and an idempotency key. The service
recomputes policy, holds, decisions and references inside the approval
transaction. Any change marks the preview stale and rejects approval. Exact
replay returns the existing receipt; changed replay conflicts.

Approval is review evidence only. A separate explicit execution request is
required before the independent retention worker can act.

## Current preview scope

- Hot history currently evaluates normalized observations.
- Raw-report preview evaluates terminal imports and their immutable object
  manifests.
- Audit preview currently evaluates finding-correlation events.
- Archived evidence candidates appear after execution creates verified archive
  manifests.

This scope is intentionally explicit rather than presenting unimplemented
archive tiers as populated.

## Verification completed

- Real PostgreSQL/S3 acceptance covers policy ordering and revisions, viewer and
  foreign-workspace denial, holds, active decisions, shared references, split
  audit history, mutation-free preview, stale hold/policy rejection, approval
  replay and exact raw-evidence preservation.
- Browser acceptance covers policy editing, hold creation, per-class impact,
  protected reasons, stale approval refresh and successful non-destructive
  approval.
- The complete 151-case browser suite passes with the lazy Settings and explicit
  observation-restoration workflows.
- The authentic pinned V11 production closure migrates through V19 while
  preserving historical API/native data and exact old business rows.

## Remaining limits

- No orphan-object garbage collection.
- No recurring-scan storage/index/WAL growth measurement.
- Preview is whole-workspace and capped at 200 resources; scoped/paginated
  enterprise preview remains future work.

See `m06-retention-execution.md` for the implemented worker, availability and
restoration boundary.
