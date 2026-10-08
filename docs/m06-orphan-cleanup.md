# M06 archive publication reconciliation

Implemented on October 7, 2026. V18 closes the known product-created archive
orphan gap without bucket-wide object discovery.

## Publication ledger

Before the retention worker writes normalized observation or correlation-event
detail to archive storage, it records the exact product key, digest, size,
resource identity and publication revision in PostgreSQL.

Publication states are:

- `publishing`: database intent exists and S3 publication/finalization may be
  incomplete.
- `referenced`: the archive key is referenced by the observation or audit row.
- `orphan`: bytes were written but the product reference was not committed.
- `deleted`: old unreferenced bytes were deleted or already missing.

Archive finalization changes the publication to `referenced` in the same
transaction as the evidence reference. A protection discovered after archive
write changes the publication to `orphan`. A process/lease interruption can
leave `publishing`, which is intentionally reviewable after the grace period.

## Preview and execution

The ordinary retention preview includes an `orphan-archive` class with action
`delete-orphan`. Only ledgered `publishing` or `orphan` rows older than 24 hours
are considered. Preview is still capped at 200 total resources.

Each item binds:

- Publication ID and revision.
- Exact workspace-scoped product archive key.
- SHA-256 digest and byte length.
- Ledger state and update time.
- Current database reference state.

An exact database reference returns `archive-reference` protection. Fresh,
foreign-workspace, referenced or non-ledgered objects are not eligible.

Execution locks the publication row, rechecks revision, state, grace and both
archive reference tables, then verifies/deletes only that exact selected key.
The row lock serializes a later product publication for the same key. Missing
objects complete idempotently as `already-missing`. A post-approval revision
change becomes `protected/state-changed`.

No bucket list, wildcard delete, unrelated prefix or operator credential is
used.

## Qualification

Real PostgreSQL/S3 acceptance covers:

- Archive writers finalizing a referenced publication.
- Old unreferenced deletion.
- Missing-object idempotency.
- Referenced object protection.
- Fresh publication grace.
- Post-approval revision fencing.
- Foreign-workspace isolation.
- Preserved referenced, fresh, changed and foreign bytes.

The retention Settings workflow shows the exact product archive key in preview
and execution status.

## Limits

- The ledger covers product publications from V18 onward. It does not discover
  arbitrary external or pre-ledger bucket objects.
- The 24-hour grace is a fixed safety constant, not a legal retention policy.
- Whole-workspace preview remains capped at 200 resources.
