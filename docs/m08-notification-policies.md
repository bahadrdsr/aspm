# M08 automatic notification policies

Implemented on October 8, 2026.

V21 adds explicit workspace automation for authoritative finding changes without
turning scans into provider calls. Administrators approve a bounded policy
revision, imports record eligible change events, and the independent delivery
worker later evaluates one event into the existing durable outbox.

## Policy approval

Workspace members can read policies and immutable evaluation history. Only an
administrator can create or revise a policy. Each approval records:

- A name and enabled state.
- One current Slack, Jira, or Teams connection snapshot.
- A nonempty subset of `new`, `changed`, and `reopened`.
- A minimum severity.
- The actor, rationale, revision, workspace policy epoch, and time.

The current policy row is paired with an immutable full revision. A connection
revision change makes the old snapshot stale until an administrator explicitly
reviews and saves another policy revision. A workspace has at most 32 policies.

## Change events

Only successful full, complete scans with a source timestamp can create policy
events. The event contains the canonical finding ID, bounded title, severity,
asset name, change kind, change revision, time, and the current policy epoch.
It excludes evidence, notes, remediation, unmapped source fields, credentials,
and scanner extras.

Workspaces with no approved policy epoch create no policy event. Failed,
partial, delta, unknown-time, historical, unchanged, and inferred-resolution
observations also create no event. Import success never depends on a running
delivery worker or provider.

## Evaluation and delivery

The delivery worker evaluates one durable event transactionally before any
provider request. It uses the latest immutable policy revision that existed at
the event epoch. Later policy creation or edits do not apply retroactively.

A matching policy creates a normal queued delivery with:

- The policy and finding change revisions.
- The approving actor as `requestedBy`.
- The exact connection snapshot.
- A stable policy idempotency key.
- A bounded title, severity, asset, change kind, and trusted finding link.

Evaluation outcomes are `queued`, `duplicate-ticket`, `connection-stale`, or
`invalid-payload`. Queued means durable outbox admission, not provider delivery.
There is no automatic retry, provider success claim, finding mutation, or
verification inference.

## Jira duplicate prevention

Jira uses one durable effect key per workspace, connection, and finding. V21
backfills the earliest existing Jira intent and uses the same key for manual and
policy-created work. Replaying the original manual idempotency key still returns
the original delivery. A different key conflicts instead of creating another
ticket. Any prior Jira intent, including uncertain or failed work, conservatively
prevents automatic duplicate creation and is referenced by the policy outcome.

## UI and limits

Integrations includes a Notification policies section. Administrators can review
the native connection, triggers, severity threshold, enabled state, and rationale.
Viewers see read-only policy and event history. Stale and duplicate outcomes are
explicit, and queued outcomes are labeled as not delivered.

Generic outbound webhooks, live-provider qualification, provider status linkage,
automatic finding resolution, bulk risk acceptance, and policy/event retention
remain separate work.
