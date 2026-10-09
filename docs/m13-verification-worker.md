# M13 deterministic verification worker runtime

Implemented on October 9, 2026.

`cmd\verification-worker` is the standalone runtime for V27 deterministic
verification jobs. It is database-only. It does not receive S3 credentials,
provider configuration, a target, browser, shell, subprocess, script, scanner,
or arbitrary tool authority.

## Environment

Required:

- `ASPM_DATABASE_URL`

Defaults and bounded overrides:

- `ASPM_SCHEMA=aspm`
- `ASPM_DB_MAX_CONNECTIONS=1`
- `ASPM_LISTEN=127.0.0.1:18080`
- `ASPM_VERIFICATION_LEASE_DURATION=90s`
- `ASPM_VERIFICATION_AUTHORIZATION_INTERVAL=100ms`
- `ASPM_VERIFICATION_MAX_FIXTURE_BYTES=65536`

The existing direct HTTPS listener remains optional through
`ASPM_TLS_CERT_FILE` and `ASPM_TLS_KEY_FILE`.

Every process generates a fresh lower-case 32-hex durable worker ID. Operators
cannot supply `ASPM_WORKER_ID`; the human-readable PostgreSQL application name
remains `aspm-verification`.

## Health and readiness

The worker serves only `/healthz` and `/readyz`. Readiness means:

- database reachable
- schema compatible
- storage not required

It does not claim that a verification was processed, a finding is safe, a
provider is available, a target is reachable, or work capacity is available.
Operators must inspect the durable job state separately.

Synthetic fixture reproduction is not proof of a real vulnerability and does
not classify the finding. A reproduced or not-reproduced result does not close
a finding.

## Deployment

Helm keeps `verification.enabled: false` by default. Enabling it creates one
independent database-only Deployment that runs
`/app/bin/verification-worker`, with no service account token, storage Secret,
provider setting, Service, ingress, RBAC object, or init container.

The shipped Quadlet is a manual opt-in at
`deploy/quadlet/aspm-verification.container`. Put protected runtime values in
`/etc/aspm/verification.env`. The Linux installer does not copy, provision,
activate, or start this unit.

## Recovery evidence and limits

Owned runtime acceptance kills a process while its fenced publication
transaction is blocked. The processing lease survives, a fresh process reclaims
the job with a distinct worker ID, and exactly one terminal result commits.
Another empty-queue restart leaves the terminal job and finding unchanged.

This increment does not certify backup or restore procedures. High availability,
performance, signing, release certification, production network isolation, and
enterprise capacity remain broader M13 release gates.
