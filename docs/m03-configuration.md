# Installer configuration and bounded execution

Implemented: an offline, versioned configuration resolver and `aspmctl init`,
`plan`, `doctor`, and `version`, plus the bounded operational execution API and
`deploy-plan`, `apply`, `status`, and `uninstall` commands described below.

The short terminal flow collects destination, necessary access, and confirmation.
It writes full reproducible JSON configuration, retaining secret references
rather than resolving or generating credentials. Defaults come from the same
embedded M00 schema/default artifacts tested in `contracts`; no remote schema
lookup or competing configuration vocabulary is used.

`aspmctl plan --config install.json` produces a stable `config-sha256:` identity.
Changed target or secret reference changes that identity. Invalid targets,
inline secrets, unsafe storage/exposure, or excessive connection allocation
fail without a usable plan. This is a configuration preview, not target
inspection, artifact verification, release approval, or permission to mutate
infrastructure.

`aspmctl doctor --url <origin> --ca <trusted-ca-file>` checks process/dependency
responses with normal TLS verification. HTTP is permitted only for literal
loopback development addresses. It refuses redirects and does not interpret a
healthy HTTP endpoint as current security posture.

Independent M03 configuration tests passed before CLI smoke verification.
The three-stage scripted terminal flow, round-trip planner and live diagnostic
calls were exercised. These configuration-only checks do not certify operational
deployment. Backup, restore, provisioning, arbitrary upgrades and unsupported
execution profiles fail explicitly rather than pretending to install.

The local HTTPS validation certificate and account created by the explicit
`scripts/local-smoke.py --enroll` test are test artifacts outside the repository,
not shared production defaults. No OS trust store is changed automatically.

## Bounded operational execution

`internal/install/execution.Open(ctx, Options)` returns `Plan`, `Execute`,
`Status`, and `Close`. It uses the existing `install.Resolve` result unchanged.
Deployment approval is a separate `deployment-sha256:` ID binding that config ID,
the exact independently signed bundle and image identities, actual inspected
cluster/machine fingerprint, operation/delete-data selection, runtime Secret
references/scopes, and private caller role-key identities. Kubernetes ownership
uses the inspected cluster UID and selected namespace, not kubeconfig formatting,
context labels, or authentication bytes. Those caller inputs are bound separately
to each approval: renewal needs fresh approval but does not change ownership or
rotate application secrets.

Supply an existing execution root, a signed bundle beneath it, an independent
Ed25519 `PUBLIC KEY` PEM file outside that bundle, nonsecret runtime selections,
and an owner-only role-key JSON file. The latter contains `core` and `ingestion`,
each with explicitly supplied `accessKey` and `secretKey` strings. Values must
be distinct across both roles; no ambient/operator key fills a missing input.
Do not put credential values in command arguments or configuration previews.

The shared operational flags are:

```text
--root <existing-directory>
--config <root-relative-installation.json>
--bundle <root-relative-signed-bundle-directory>
--runtime-roles <root-relative-runtime-roles.json>
--role-keys <root-relative-private-role-keys.json>
--bundle-trust <independent-public-key.pem>
--trust-fingerprint sha256:<optional-independent-SPKI-fingerprint>
--kubeconfig <root-relative-explicit-kubeconfig.json>
```

`deploy-plan` inspects the selected target without writes or application-secret
resolution. Use `deploy-plan --operation uninstall` for its distinct uninstall
approval, adding `--delete-data` only for explicitly selected destructive cleanup.
`apply --dry-run` does the same without approval and returns
`planned`, never `applied`. `apply --approve-plan <deployment-id>` applies that
exact freshly revalidated intent. `status` reads its durable checkpoint and needs
the root/private-input/trust flags, not a new deployment intent. `uninstall`
requires its own inspected plan; `--delete-data` changes the plan ID again.
The existing `plan --config ...` remains configuration-only.
Mutating commands print completed-step messages followed by their state JSON.
Use `status` for a standalone JSON checkpoint response in automation.

The initial caller profile accepts explicit **JSON** kubeconfig with a selected
context and HTTPS cluster, using a static bearer token/token file or a matching
client certificate/key pair. CA and certificate inputs are validated; insecure
TLS, exec/auth-provider plugins, unsupported authentication, YAML and ambient
kubeconfig fallbacks are rejected before tools. Native commands always carry
explicit kubeconfig/context/namespace. No shell, sudo, SSH, package installer,
post-renderer or uploaded command runs. Helm plugins and credential-bearing
ambient environment variables are disabled in the CLI child environment.

For local Linux, the CLI derives actual OS/EUID and requires Linux, EUID 0,
`localhost`, and the real filesystem root. It does not simulate privileges,
elevate, provision a host, or apply a staged root to the real system manager.
The initial managed profile has matching parser/reconciler replica selections
and per-role database caps; Linux has one instance of each role. External
services, mirror/offline retrieval, supplied application TLS material, native
Linux data-volume deletion and unsupported sizing/topology need separate work.

Generated application secrets are created only after valid deployment approval,
retained in owner-only files, and reused on retry/reapply. File references are
availability-checked during planning, not read for secret values. The initial
application file formats are a raw bootstrap token, a raw PostgreSQL password,
and operator storage JSON with `accessKey`/`secretKey`. Operator policy is private
and separate from application credentials. Changing an existing secret reference
or role-key bytes requires a separate rotation workflow, not silent replacement.

Kubernetes applies separate database/bootstrap, core-storage, ingestion-storage
and `aspm-storage-policy` Secrets through stdin. JSON Helm values contain only
references and the exact selected scopes and signed image identities. The
verified owned chart is supplied as the exact private snapshot described below,
with `upgrade --install --wait` and a bounded timeout. Approved managed
installation sets `core.prepareReadiness=true` in Helm values and literal
`ASPM_S3_PREPARE_READINESS=true` in Linux `core.env`. Ingestion and reports
receive no preparation flag. This opts in only the core's already-selected
readiness key using its existing narrow raw-scope credential.
Linux uses the pure Quadlet role renderer, separately pins signed images, and
retains unrelated unit/network/volume directives.
`core.env`, `ingestion.env`, `reports.env`, `postgres.env`, `s3.json` and the
private credential-set file are not public logs or commit material.
Podman env files retain the literal bytes after the first `=`. Quotes, backslashes
and interior `=` are values, not shell syntax; no wrapping or escaping is added.
CR/LF/NUL line injection is rejected before materialization.

Checkpoints are owner-only and atomically renamed. Failed native commands retain
completed steps and record `failed`/`command-failed`; resume rechecks trust and
target before retry. Reapply uses the same credential bytes and unique checkpoint
names. Default uninstall preserves data and credentials. Separately approved
Kubernetes deletion only targets the two named owned PVCs and selected owned
Secrets, never namespaces, PVs, `--all`, or unrelated names. Private recovery
material is retained even after cluster data deletion. One installer writer per
root is the initial profile; cross-process installer locking is not certified.

## Native startup gaps, not waived by module tests

- Managed installation explicitly enables the core's conditional readiness
  preparation. The selected database and bucket must still become available,
  and the signed runtime image must support the flag. A tool-return success
  alone does not prove that the object exists or that role-signed access works.
- The parent-owned chart now maps `storage.policySecret.name`/`key` (selected as
  `aspm-storage-policy`/`s3.json`) to the storage control-plane mount, rather than
  using the common application Secret. Actual rendering, mounting and native
  startup remain the parent's independent deployment validation.
- Quadlet-generated services are transient: the installer preserves source
  `[Install] WantedBy`, runs `daemon-reload`, and starts exact generated service
  names with `--no-ask-password`. It does not use `systemctl enable` on them.
- Linux starts PostgreSQL and storage before application roles. `systemctl start`
  completion is not a SQL/S3 readiness test, and persisted Quadlet dependency/
  isolation settings still need native validation and confirmed core readiness
  preparation before dependent workers are considered usable.
- HTTPS access/trust establishment, database least-privilege roles, native owner
  and ACL enforcement, readiness checks, cross-process locking, backup/restore,
  image execution, upgrades, HA and release/CI certification remain separate
  gates. In particular, Windows owner-only ACLs are not certified here.

## V2 execution corrections and compatibility

Helm consumes a private, content-addressed `.tgz` snapshot of the same complete
approved chart, generated solely from verified file bytes. It does not load the
mutable input chart directory or undeclared templates. The archive admits only
regular files beneath one chart root, with bounded membership/size and validated
paths. A pre-existing snapshot with different bytes is rejected. Every step and
native command revalidates the bundle against the exact approved digest; a
different validly signed bundle of the same release cannot reuse old approval.

Linux uninstall verifies recorded owned definition hashes, stops only their
generated services, removes those container activation definitions, and reloads
the manager. Unrelated units, network/volume definitions, actual volume contents
and all credential bytes are preserved by default. Changed definitions require
explicit investigation rather than blind deletion.

New checkpoints and private material use format version 2. Version-1 public
status can be inspected, but operational reuse is explicitly unsupported: its
caller-bound hash cannot safely prove stable cluster ownership after caller
changes. No automatic reset, relabeling or secret rotation is attempted. A
separately reviewed ownership/material migration is required for those records.

Private rooted staging and immediate validation remove mutable-directory loading
from this workflow; they do not defeat a same-privilege OS actor able to rewrite
files between validation and native consumption. Cross-process locking and
native filesystem ownership/ACL guarantees remain separate validation.

## Owned Kubernetes qualification on September 16, 2026

The actual Windows-hosted CLI was exercised against a separate, initially absent
namespace in the owned Kubernetes lab. The bundle contained exact copies of the
shipped deployment files and independently supplied qualification signing trust.
Image references used verified OCI manifest/index digests, not container
configuration or status identifiers. The storage identity remained the one
required by the versioned schema; no signature or schema check was bypassed.
Native container-runtime resolution was confirmed on both lab nodes.

- The real three-stage wizard saved the configuration. Native `deploy-plan`
  inspected the explicit cluster, and `apply --dry-run` preserved execution-root
  inputs without creating application secrets, deployment state or a namespace.
- Approved `apply` created the namespace, separate Secrets, private recovery
  material and a ready Helm deployment. Only core received the preparation flag,
  and caller role-key bytes reached the selected Secrets unchanged. Dependencies
  initially caused application-pod restarts before startup completed.
- Real HTTPS first-user setup, login, assets, asynchronous import, independent
  reconciliation, report snapshots and logout worked. HTTPS termination used a
  private loopback fixture, not a qualified production ingress controller.
- A reopened CLI reapply retained exact credential/environment bytes and unique
  checkpoints. Equivalent caller-file reformatting retained stable ownership;
  fresh approval worked without application-key rotation.
- Default uninstall removed workloads and retained both original PVC identities,
  all selected Secrets and private recovery material. Reinstall preserved the
  original account, workspace, asset/import identities, byte-exact raw evidence
  and immutable report snapshot through their authenticated APIs.
- Separately approved data deletion rejected an ordinary uninstall approval.
  A controlled missing-Helm PATH caused a real native command failure with a
  persisted failed checkpoint. Restoring the tool and reopening the CLI resumed
  cleanup, deleting only the named owned PVCs and Secrets. Private recovery
  material and the empty namespace remained; temporary verification processes
  were stopped. The original application deployment remained healthy.

This qualifies that managed Kubernetes path, not arbitrary clusters, native
Linux activation, production TLS/network policy, database role isolation,
backup/restore, historical checkpoint migration, filesystem race guarantees or
enterprise capacity/HA. The private qualification bundle is not a published
release and its signing authority is not a production trust root.

The operational `applied` phase means the selected native tool steps completed.
It is not a claim of a ready production installation or full M03 completion.
