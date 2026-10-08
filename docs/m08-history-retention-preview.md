# M08 history retention preview

Implemented on October 8, 2026.

V23 extends the existing M06 retention hold, preview, approval, and Settings
workflow to four M08 audit resources:

- Finding decision events.
- Notification policy revisions.
- Finding change events.
- Notification policy evaluation events.

All four use the workspace audit retention period and the existing
`archive-audit` preview action. The preview reports exact logical UTF-8 bytes
for a fixed canonical JSON payload. It performs no provider calls, object-store
I/O, event mutation, deletion, or physical storage-savings estimate.

## Holds and protection

Administrators can create and release workspace-scoped holds for each new
resource kind. Viewers can read hold history and create retention previews but
cannot change policy, holds, approvals, or execution.

Preview protection reasons are explicit:

- `legal-hold` for an active exact-resource hold.
- `current-policy-revision` for the current policy revision.
- `pending-policy-evaluation` for pending/processing finding changes and the
  policy revisions those changes still require.
- `active-delivery` for evaluation events linked to queued or dispatching
  deliveries.

Protection reasons, payload fields, current policy identity, processing
fence/lease state, linked delivery state, and candidate membership are included
in the preview binding. Any relevant change makes approval stale.

## Preview-only execution boundary

The existing 200-resource whole-workspace cap, 15-minute expiry, exact
approval digest, rationale, idempotency, and replay behavior remain unchanged.

An approved preview containing any V23 history resource is deliberately
preview-only. Attempting to queue it returns `409 preview-only` before creating
a retention run or item. Legacy-only previews retain the existing M06 execution
path. History archive execution and retrieval are deferred to V24.

## Migration

Schema V23 widens only the retention hold and preview-item resource-kind
constraints and adds one bounded workspace/time candidate index for each M08
event table. It does not widen retention run items, add archive columns, add
relations, or change worker actions.
