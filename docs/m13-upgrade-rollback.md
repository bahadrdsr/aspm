# M13 upgrade and rollback

Implemented on October 10, 2026.

This is the supported technical preview procedure for replacing one signed ASPM
installer bundle on the same selected Linux host or Kubernetes namespace. It is
not a downgrade framework, a database down-migration system, a zero-downtime
claim, or an automatic rollback controller.

## Required preparation

Before approving an upgrade:

1. Stop unrelated deployment changes and retain the current signed bundle,
   installation configuration, role selection, trust key, and private role-key
   file.
2. Complete and verify a quiesced backup using
   `docs\m13-backup-restore.md`.
3. Verify that the new configuration names the new bundle release exactly and
   that the bundle is signed by the independently selected trust key.
4. Keep the previous signed bundle and its matching configuration available
   until the upgrade and post-upgrade checks are complete.

The installer accepts explicit semantic release strings such as
`0.1.0-rc.1`. The signed bundle release, installation configuration release,
and pinned application image must describe the same approved target.

## Plan and apply

Use the ordinary deployment flags for the selected target and add the new
configuration and bundle:

```powershell
aspmctl deploy-plan --operation apply <deployment flags>
aspmctl apply --approve-plan <exact-plan-id> <deployment flags>
aspmctl status <deployment flags>
```

The plan and status output expose:

- current and target release;
- current and target signed bundle digest;
- change kind (`initial-install`, `reapply`, `upgrade`, `rollback`, or
  `checkpoint-migration`);
- the rollback policy and current rollback mode.

Installer checkpoint version 3 stores this lineage. An existing version 2
checkpoint requires an explicitly approved `checkpoint-migration` plan. That
migration records the currently selected signed bundle and performs no native
deployment command.

## Rollback boundary

An upgrade begins in `activation-eligible` mode. If it fails before the new
application activation boundary, the exact previous signed bundle may be
reactivated:

```powershell
aspmctl deploy-plan --operation rollback <previous deployment flags>
aspmctl rollback --approve-plan <exact-rollback-plan-id> <previous deployment flags>
```

The previous configuration must match the retained previous bundle, logical
target, runtime roles, and installer checkpoint. A stale plan, changed caller
identity, changed target, different previous bundle, or untrusted signature is
rejected before mutation.

For Kubernetes, the activation boundary is the Helm upgrade that can start the
new application image. For Linux, it is starting the application role services
after the signed Quadlet definitions are installed. Immediately before either
boundary, the durable checkpoint changes to `data-restore-required`.

If activation was attempted, if the upgrade succeeded and the application ran,
or if rollback activation itself failed, do not start an old binary against the
current database. ASPM migrations are forward-only. Stop all ASPM processes and
restore the verified pre-upgrade backup into an absent schema and empty selected
prefixes on a new recovery target. The recovery endpoint and credentials may
change, but the logical instance, database/schema, bucket, and prefix names must
remain the same.

## Post-upgrade checks

After apply succeeds:

1. Confirm `aspmctl status` reports the intended release and bundle digest.
2. Confirm the application and enabled workers are ready.
3. Authenticate with a new session and run an ordinary asset, import, finding,
   report, retention, and deterministic verification smoke flow.
4. Verify the migration ledger remains exact and contains no duplicate version.
5. Retain the pre-upgrade backup through the operator's recovery window.

The current binary rejects a schema newer than V27. This procedure does not
claim compatibility with an unknown future schema, cross-target migration,
stateful high availability, online database rollback, or retained user sessions.
