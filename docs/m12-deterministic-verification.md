# M12 deterministic verification

Implemented on October 9, 2026.

V27 adds an explicitly synthetic, approval-bound verification workflow. It is
not an exploit engine, scanner, browser runner, shell, target probe, provider
call, or autonomous offensive workflow.

## Safe evidence boundary

The only accepted fixture schema is:

```json
{"schema":"aspm.synthetic-fixture/v1","environmentId":"...","condition":true}
```

The service canonicalizes and stores exact UTF-8 JSON bytes in PostgreSQL.
Fixture content is immutable, bounded to 64 KiB, and never returned by list or
detail APIs. Environment and scope revision remain external immutable bindings;
scope revision is not a fixture field.

Admins and analysts may submit a fixture for a current finding. The stored
record binds the current finding evidence revision, method, environment, scope,
digest, size, submitter, and creation time.

## Explicit approval

Only a current workspace administrator can approve submitted evidence.
Approval binds:

- Workspace and finding.
- Evidence ID and SHA-256 digest.
- `deterministic-evidence` method.
- Environment and scope revision.
- Current finding evidence revision.
- Approver, rationale, creation, and expiry.

Expiry must be within 24 hours. An approval is current only while it is not
revoked, has not expired, and still matches the finding evidence revision.

Revocation is explicit and immutable. It records the revoker, time, and
rationale, cancels queued or processing bound jobs, increments their fence, and
does not rewrite succeeded or other historical terminal jobs.

## Durable verification jobs

Admins and analysts can queue one job using an exact approval and a
workspace-scoped idempotency key. A 202 response means queued only. Exact
replay returns the original receipt and requester; a changed binding conflicts.
Viewers have read-only history and detail access.

The standalone `OpenVerificationWorker` surface is database-only. It receives
no S3, target, provider, browser, subprocess, or tool configuration.

`ProcessNext` claims at most one job using a database-clock lease, three
attempts, and monotonic fencing. It reads immutable fixture bytes through a
bounded database evidence reader, resolves current approval through trusted
database state, invokes `internal/verification`, and atomically publishes only
under the current live worker and fence.

Pinned terminal meanings are:

- `succeeded/reproduced`: the approved synthetic condition is true.
- `succeeded/not-reproduced`: the approved synthetic condition is false.
- `cancelled/approval-revoked`: approval was explicitly revoked.
- `blocked/approval-expired`: approval expired.
- `blocked/binding-changed`: finding evidence or immutable binding changed.
- `failed/fixture-format`: fixture bytes are not the exact supported schema.
- `failed/evidence-integrity`: size or SHA-256 does not match.
- `failed/attempt-limit`: an expired third lease cannot be reclaimed.

No outcome changes finding workflow, disposition, source state, false-positive
state, AI state, independently verified resolution, or closure.

## Finding UI

Finding detail includes a closed-by-default **Deterministic verification**
section. Opening it explicitly loads the first evidence, approval, and job
pages. It never polls.

Admins can submit, approve, revoke, queue, read, and refresh. Analysts can
submit, queue against a current approval, read, and refresh. Viewers can read
and refresh only.

History uses explicit Refresh and native Load more. Detail state changes only
after **Refresh verification**. An ambiguous queue acknowledgement retains one
in-memory intent and exact key until authorized history reconciliation. It is
not retried automatically or stored in browser storage.

The UI always states:

`Synthetic fixture reproduction is not proof of a real vulnerability and does not close or classify the finding.`

## Qualification boundary

Real PostgreSQL acceptance covers exact V27 migration and V26 projection,
canonical bytes, authority, approval/revocation, idempotency, native paging,
fixture nondisclosure, safe outcomes, expiry and binding changes, active
revocation, fencing, reclaim, integrity, format failure, and attempt limits.

Browser acceptance covers all three roles, lazy reads, manual state refresh,
lost acknowledgement reconciliation, denial latching, malformed-response
rejection, workspace/session cancellation, storage privacy, focus, reduced
motion, and a 390-pixel viewport.

This slice proves only the deterministic handling of approved synthetic
fixtures. It does not prove a real vulnerability exists or is remediated.
