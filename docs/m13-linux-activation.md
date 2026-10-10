# M13 native Linux activation

Qualified on October 10, 2026.

The signed `0.1.0-rc.1` release was activated on Ubuntu 24.04.4 LTS under WSL2
with systemd and Podman 4.9.3. This proves the native Quadlet path on that
environment. It is not a claim for every Linux distribution, Podman version,
kernel, firewall, or bare-metal configuration.

## Qualified flow

- Loaded the signed OCI archive as the exact
  `localhost/aspm:0.1.0-rc.1@sha256:...` identity.
- Verified a read-only plan and dry run before owner-only files or native
  resources existed.
- Applied the signed installer bundle against the actual `/` filesystem.
- Published the SeaweedFS policy through a Podman secret. The source file
  remained root-only and the container mount was UID/GID 1000, mode 0400.
- Waited for PostgreSQL 18.6 and SeaweedFS 4.47 before starting application
  roles.
- Started core, one ingestion worker, retention, and reports sequentially and
  waited for each role's readiness endpoint.
- Verified loopback health/readiness, exact V27 ledger, all six services active,
  and owner-only installer state.
- Completed real bootstrap, login, and asset creation through the public API.
- Stopped application roles, restarted retained PostgreSQL/storage, restarted
  roles, and read the same asset through a new session.
- Applied a signed synthetic `0.1.0-rc.2` upgrade plan with an intentional
  pre-activation Podman failure.
- Verified the durable state remained `activation-eligible`, then activated the
  exact previous signed bundle with `aspmctl rollback`.
- Verified the asset and exact V27 ledger remained unchanged after rollback.

## Corrected native defects

Qualification found and corrected:

- SeaweedFS drops to UID 1000 and cannot read a root-only bind file. The policy
  now uses a Podman secret mount.
- Podman 4.9 `secret create --replace` fails when the secret is absent. The
  installer uses exact `rm --ignore` then stdin-only create.
- Application roles previously raced PostgreSQL/S3 startup. Native apply now
  uses bounded dependency and role readiness probes.
- The ingestion template could create `@multi-user`. The supported
  single-instance profile now uses a dedicated non-template Quadlet.
- Grouped role starts could surface cgroup races. Roles now start and settle
  sequentially.
- A failed reapply could retain old completed checkpoints. Reapply now starts
  with a fresh step set and resumes only work completed by that attempt.
- Rollback was incorrectly routed through uninstall steps. It now activates the
  previous signed bundle.

## Limits

The qualified profile is one host, one PostgreSQL container, one SeaweedFS
container, one core, one ingestion worker, one retention worker, and one report
worker. It does not qualify SELinux enforcing hosts, rootless Podman, external
PostgreSQL/S3, multi-host scheduling, stateful HA, automatic firewall repair, or
an operating-system upgrade.
