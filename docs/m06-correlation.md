# M06 reversible finding correlation

Implemented on October 7, 2026. This is the first bounded M06 correlation
increment, not completion of candidate matching, multi-member correlation or
retention/archive.

## Supported workflow

A workspace member can open one finding, enter one other finding ID and request
a read-only merge preview. Both findings must:

- Belong to the same workspace.
- Belong to the same asset.
- Have different source identities.
- Be outside any other active correlation.

The preview returns both current decisions, decision/evidence revisions,
observation and note counts, and conflicts in owner, workflow state,
disposition or accepted-risk expiry. Preview performs no mutation.

An analyst or administrator can confirm the merge only by supplying:

- The expected decision and evidence revisions for both findings.
- The selected owner, workflow state, disposition and accepted-risk expiry.
- A non-empty rationale.
- An idempotency key.

The selected primary finding remains in Work. The active secondary variant is
hidden from Work and cannot be edited or used as a remediation target. Its row,
raw evidence, observations, notes and original decisions remain stored. Primary
detail aggregates retained observations and notes from active members.

## Reversal and audit

Only the active primary can initiate split. This increment requires exactly two
active members. Split preview is read only. Confirmation requires current
revisions, explicit post-split decisions for both findings, a rationale and an
idempotency key.

Split marks memberships released rather than deleting them, records an audit
event and restores both findings to Work. It does not move, rewrite or
duplicate raw evidence.

Every merge and split stores:

- Actor, rationale and timestamp.
- A monotonic sequence within the correlation.
- The idempotency key and SHA-256 request binding.
- Before and after state snapshots.

Exact replay returns the prior result. Reusing the key with changed input is a
conflict. Any intervening note, human decision or evidence/source-state change
invalidates a stale confirmation.

## Persistence

Additive schema V13 introduces finding decision/evidence revisions plus
correlation, membership and event tables. A partial unique index prevents one
finding from participating in more than one active correlation.

Historical V11 data is migrated through current V14 without rewriting old
migration literals or business rows outside the approved additive defaults.

## Verification completed

- Real PostgreSQL and S3 acceptance covers conflicts, role and workspace
  denial, stale previews, exact replay, visibility, aggregated history, split,
  evidence-byte preservation and reopen durability.
- Browser acceptance covers explicit preview, decision selection, merge audit,
  Work collapse, split and Work restoration.
- The complete 149-case browser suite passes with the correlation and retention
  preview/execution workflows.

## Remaining limits

- No automatic or suggested candidates.
- No similarity scoring or global all-pairs matching.
- No cross-asset or same-source merge.
- No groups larger than two active members.
- No bulk merge/split.
- Retention policy, holds and preview approval are implemented separately, but
  physical archive/expiry execution and restoration are not.

See `m06-retention-preview.md` for the current retention boundary.
