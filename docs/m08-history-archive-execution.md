# M08 history archive execution

Implemented on October 8, 2026.

V24 executes approved V23 history preview items through the existing fenced
retention worker. It archives the exact canonical JSON bytes for:

- Finding decision events.
- Notification policy revisions.
- Finding change events.
- Notification policy evaluation events.

The worker commits archive publication intent before object I/O, holds no SQL
transaction during S3 publication, verifies exact bytes, digest, and size, then
rechecks the live event and every V23 protection before attaching the object.
A changed payload or newly protected resource leaves one orphan-ledgered object
for the existing reconciliation workflow and cannot overwrite current state.

## Durable metadata and compatibility

Each history row records detail availability, a monotonic detail revision, and
the exact archive key, digest, size, and archive time. Original event payload
columns remain unchanged, so existing finding decision and notification policy
APIs retain their published bytes and semantics.

Archived rows are excluded from later retention previews. Replaying an already
archived item performs no second PUT or metadata transition. A verified object
left after a worker crash can be attached without another write.

V24 creates immutable archive copies but does not compact or delete the hot
event rows and does not claim physical database savings.

## Retrieval

Any current workspace member can explicitly retrieve one archived history
resource through:

`GET /api/v1/retention/history/{resourceKind}/{resourceId}`

The response verifies the configured archive object and returns the exact JSON
bytes with server-owned resource, availability, digest, size, and detail
revision headers. Available resources return `409 history-not-archived` without
object I/O. Missing or corrupt objects persist that observed availability and
return the existing evidence-specific errors. A later valid read can restore
the state to archived using the detail revision fence.

Retention Settings queues execution explicitly, refreshes manually, and offers
retrieval only for successful archived history items. Retrieved payloads remain
in memory, are never stored in browser storage, and are cleared on workspace or
session loss.

## Remaining limits

- Hot-row compaction and history archive expiry are not implemented.
- Archived history is retrieved one exact resource at a time.
- No recurring throughput or database-size reduction claim is made.
