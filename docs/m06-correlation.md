# M06 reversible finding correlation

Implemented on October 7, 2026. The explicit two-member workflow is extended
with bounded exact-location candidates and reversible multi-member groups.

## Supported workflow

A workspace member can request a paged candidate list for one finding. The
query is capped at 100 rows and uses the V16 partial index. A candidate must:

- Belong to the same workspace.
- Belong to the same asset.
- Have the same source branch.
- Have the same non-empty normalized source URI and positive source line.
- Have different source identities.
- Be outside any other active correlation.

The list is a suggestion only. It never merges automatically and returns the
exact match branch/URI/line. Manual finding-ID entry remains available for
reviewed same-asset cases that do not meet this exact candidate rule.

A merge preview returns current decisions, decision/evidence revisions,
observation and note counts, conflicts in owner/workflow/disposition/risk
expiry, and the current active group when the primary already owns one.
Preview performs no mutation.

An analyst or administrator can confirm the merge only by supplying:

- The expected decision and evidence revisions for both findings.
- The selected owner, workflow state, disposition and accepted-risk expiry.
- A non-empty rationale.
- An idempotency key.

The first merge creates a two-member group. Later reviewed merges can add one
uncorrelated finding at a time to the active primary, up to the schema's
64-member historical bound. Active members must have distinct source
identities. The selected primary finding remains in Work. Active secondary
variants are hidden from Work and cannot be edited or used as remediation
targets. Their rows, raw evidence, observations, notes and original decisions
remain stored. Primary detail aggregates retained observations and notes from
active members.

## Reversal and audit

Only the active primary can release a member. Split preview is read only.
Confirmation requires the current correlation, decision and evidence revisions,
explicit post-release decisions for the primary and selected member, a
rationale and an idempotency key.

With more than two active members, release restores only the selected member to
Work and keeps the remaining group active. Releasing the final secondary marks
the group split and restores both last active findings. Membership history is
released rather than deleted. Raw evidence is never moved, rewritten or
duplicated.

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
finding from participating in more than one active correlation. V16 adds
default-empty candidate URI/line columns and a partial
workspace/asset/branch/location index. Historical findings are not rewritten;
their candidate signature is populated by the next admitted observation.

Historical V11 data is migrated through current V16 without rewriting old
migration literals or business rows outside the approved additive defaults.

## Verification completed

- Real PostgreSQL and S3 acceptance covers conflicts, role and workspace
  denial, stale previews, exact replay, bounded indexed candidates, same-source,
  branch, line and asset exclusions, three-source groups, partial member
  release, 15 retained observations, evidence-byte preservation and reopen
  durability.
- Browser acceptance covers candidate review, manual fallback, adding a third
  source, active/released history, partial release and final Work restoration.
- The complete 151-case browser suite passes with both two-source and
  three-source correlation workflows.

## Remaining limits

- No automatic merge and no global all-pairs matching.
- No fuzzy title, semantic, path-alias or line-range similarity.
- No cross-asset or same-source merge.
- Suggested candidates require an exact current branch/URI/line signature.
- Historical rows need a later admitted observation before they can be
  suggested.
- A released historical member cannot be re-added to the same still-active
  correlation.
- No bulk merge/split.
- Automatic correlation remains separate from reviewed candidate discovery.

See `m06-retention-execution.md` for the current retention boundary.
