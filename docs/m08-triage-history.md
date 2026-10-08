# M08 bulk triage and decision history

Implemented on October 8, 2026.

This increment completes the first bounded M08 triage gap without expanding the
deferred integration scope. Human workflow now includes `pending-retest`, Work
supports explicit bounded bulk assignment/workflow changes, and every accepted
finding decision update records durable actor and before/after history.

## Workflow and bulk API

`PATCH /api/v1/findings/{id}` keeps the existing owner, workflow, disposition,
and risk-expiry fields and accepts an optional `rationale`. Workflow values are:

- `open`
- `in-progress`
- `pending-retest`
- `resolved`

`PATCH /api/v1/findings` applies one atomic bulk action:

```json
{
  "findingIds": ["32-character-finding-id"],
  "ownerId": "32-character-user-id-or-null",
  "workflowState": "pending-retest",
  "rationale": "Why this selected set is changing"
}
```

The request must select 1 through 100 distinct visible findings in the current
workspace, include an owner and/or workflow change, and provide a nonblank
NUL-free rationale of at most 8,192 UTF-8 bytes. The service locks IDs in
deterministic order and commits all selected updates or none. A foreign,
secondary-hidden, deleted, or unknown finding returns not found without a
partial update. Bulk risk acceptance is intentionally not available.

The response contains the updated canonical Work items. The UI applies those
acknowledgements without waiting for a broad refresh, then clears the completed
selection. Viewer selection remains read only.

## Decision history

V19 adds `app_finding_decision_events`. Each single or bulk update records:

- The resulting monotonic decision revision.
- Actor ID and current actor name.
- `update` or `bulk-update` action.
- Exact rationale.
- Changed field names.
- Before and after owner, workflow, disposition, and risk expiry.
- The event time.

Finding detail returns up to 100 events ordered by decision revision. Additional
pages use a fixed-width revision cursor through `decisionsCursor`. Decision
history is workspace-authorized and visible to viewers, but only administrators
and analysts can create events through approved finding mutations.

Source observations, scanner-inferred state, AI conclusions, proof outcomes,
human workflow, and risk disposition remain separate. A new scan preserves
`pending-retest`; it does not resolve, verify, or add a human decision event.

## UI

The selected-findings toolbar supports:

- Assign to me.
- Unassign.
- Open.
- In progress.
- Pending retest.
- Resolved.

A rationale is required for bulk actions. Single workflow changes expose an
optional rationale. Finding detail shows a paged Decision history section with
actor, revision, rationale, changed fields, and readable before/after values.

## Developer handoff

`GET /api/v1/findings/{id}/handoff` returns at most 128 KiB of permission-checked
plain text for the selected workspace. It includes current human/source state,
scope, source-provided remediation clearly labeled as untrusted data, and up to
100 observation IDs, source/run identities, digests, availability states, and
locations. Correlated primary findings include active member observations.

The handoff excludes raw evidence text and bytes, analyst notes, unmapped source
fields, credentials, provider configuration, and executable instructions. A
response header reports whether bounded source text or evidence references were
truncated. Finding detail exposes one explicit Copy developer handoff action
with visible clipboard success or denial feedback.

## Compatibility and limits

V19 only widens the existing workflow-state CHECK and adds the decision-event
table/indexes. V20 widens the disposition CHECK and adds immutable disposition
approval records for accepted risk, suppression, and false-positive decisions.
Historical findings and decisions are not rewritten. Existing
published V5, V6, V7, V8, V9, V10, and V11 fixtures migrate through V20 with
their business rows preserved.

Accepted risk and false-positive approvals are finding-scoped. Suppression can
be scoped to a finding, source, scan scope, or asset and requires a future
expiry. Every create, change, and clear requires a human rationale. New scans,
AI output, proof non-reproduction, and scanner-inferred resolution cannot create
or remove an approval. Correlation recreates the selected approval semantics on
the target finding rather than reusing another finding's approval identity.

V21 adds explicit admin-approved automatic notification policies, immutable
policy revisions, authoritative meaningful-change events, durable evaluation
outcomes, and shared Jira duplicate-ticket prevention. Scan commits perform no
provider I/O. The independent delivery worker evaluates the policy revision that
existed at the event epoch and queues the existing outbox. See
`m08-notification-policies.md`.

V22 adds the separate generic outbound webhook profile with operator-owned
origin allowlisting, encrypted HMAC secrets, fixed signed JSON, explicit local
review/queue consent, honest accepted/uncertain outcomes, and notification-policy
selection. It remains outside the eight native catalog families. See
`m08-generic-webhooks.md`.

V23 extends retention holds and exact audit preview to finding decision events,
notification policy revisions, finding-change events, and policy evaluation
events. Approval remains non-destructive, and history archive execution is
explicitly deferred to V24. See `m08-history-retention-preview.md`.

Still separate:

- Bulk risk acceptance.
- M08 history archive execution and retrieval.
- Deferred live-account and status-linkage integration follow-ups.
