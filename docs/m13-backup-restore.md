# M13 single-instance backup and restore

Implemented on October 9, 2026.

This is the supported technical preview recovery workflow for one quiesced
self-hosted ASPM instance. It covers the exact V27 PostgreSQL application schema
and three explicitly selected S3-compatible prefixes. The qualified object
storage target is SeaweedFS 4.47.

It is not an online distributed snapshot, point-in-time recovery, downgrade,
cross-target migration, cloud snapshot certification, HA design, or RTO/RPO
claim.

## Private configuration

Create an owner-protected `recovery.json` with:

- `apiVersion: "aspm.dev/recovery/v1alpha1"`
- `kind: "RecoveryConfiguration"`
- A stable operator-created instance UUID.
- Target kind `linux` or `kubernetes`.
- One explicit PostgreSQL URL, database name, and schema.
- One explicit S3-compatible endpoint, region, bucket, static credential, and
  disjoint raw, normalized, and archive prefixes.
- Absolute paths to PostgreSQL 18.6 `pg_dump`, `pg_restore`, and `psql`.
- Explicit database, object-count, per-object, and total-object byte limits.

The database password and storage credentials are read only from this file.
They are not accepted on command arguments or from ambient PostgreSQL, AWS
profile, metadata-service, database-discovery, or bucket-discovery settings.

The portable manifest contains no database URL, password, access key, secret
key, session cookie, runtime secret, absolute host path, or tool path.

## Backup

Stop every ASPM application and worker process. Leave PostgreSQL and the
S3-compatible service available.

```powershell
aspmctl backup plan --config recovery.json --backup C:\backups\aspm-2026-10-09
aspmctl backup create --config recovery.json --backup C:\backups\aspm-2026-10-09 --approve-plan <plan-id>
aspmctl backup verify --config recovery.json --backup C:\backups\aspm-2026-10-09
```

The plan is read-only and creates no directory. Backup creation rechecks the
plan and refuses running ASPM processes, processing jobs, an unexpected schema,
an incomplete V27 ledger, unsafe prefixes, changed tools, or an existing
destination.

The workflow:

1. Exports one repeatable-read PostgreSQL snapshot.
2. Creates a custom-format schema dump without owners or ACLs.
3. Omits data from sessions, incomplete OIDC flows, and authentication throttle.
4. Records row counts and canonical row digests for every durable table.
5. Copies only the selected current objects into content-addressed SHA-256
   blobs.
6. Lists and hashes the complete object scope a second time.
7. Rechecks quiescence and verifies every artifact.
8. Atomically publishes the completed backup directory.

The manifest states `distributedAtomic=false`. No distributed atomicity is
provided. Quiescence plus one database snapshot and two matching object passes
are the supported consistency boundary.

## Restore

Restore only into an absent selected schema and empty selected prefixes. The
destination must use the same logical instance ID, target kind, database name,
same logical schema, bucket name, and same logical prefixes. The endpoint, host,
and credentials may change.

```powershell
aspmctl restore plan --config recovery.json --backup C:\backups\aspm-2026-10-09 --state C:\restore\state.json
aspmctl restore apply --config recovery.json --backup C:\backups\aspm-2026-10-09 --state C:\restore\state.json --approve-plan <plan-id> --confirm-instance <instance-id>
aspmctl restore verify --config recovery.json --backup C:\backups\aspm-2026-10-09 --state C:\restore\state.json
```

The restore plan is read-only. Apply repeats the full preflight, writes a
durable journal, creates objects with create-only semantics, verifies each
object, restores the absent PostgreSQL schema, and compares every durable table
and selected object with the completed backup. Success is reported only after
the journal reaches `verified`.

Users must reauthenticate after restore. Runtime secrets, TLS keys, bootstrap
tokens, OIDC client secrets, provider keys, and the integration-encryption key
must be reprovisioned out of band. Application-encrypted ciphertext is retained
and is usable only with the same separately protected integration key.

## Interrupted work

Partial backup directories contain a bounded marker and require their exact
backup ID for cleanup.

An interrupted restore remains `applying` or `failed`. Cleanup has a separate
read-only plan and separate approval. It deletes only journal-recorded objects
whose current digest still matches and drops only the exact schema created by
that journal. It never deletes a bucket or prefix, touches a preexisting schema,
uses wildcard deletion, or overwrites modified data.

```powershell
aspmctl restore cleanup --config recovery.json --backup C:\backups\aspm-2026-10-09 --state C:\restore\state.json --dry-run
aspmctl restore cleanup --config recovery.json --backup C:\backups\aspm-2026-10-09 --state C:\restore\state.json --approve-plan <plan-id> --confirm-instance <instance-id>
```

## Deployment targets

For Helm, scale every ASPM Deployment to zero while leaving the selected
PostgreSQL and storage StatefulSets available. Restore into a new storage-only
release. Do not start or scale application roles until restore verification
succeeds.

For Quadlet, stop the exact ASPM application and worker services while retaining
`aspm-postgres.service` and `aspm-storage.service`. Do not start application
roles until restore verification succeeds.

This workflow does not certify online multi-replica backup, high availability,
performance, signing, release certification, or enterprise disaster recovery.
