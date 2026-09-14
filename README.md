# aspm

Self-hosted application security posture management, working name **aspm**.
Module: `github.com/bahadrdsr/aspm`. Original project and SDK code: Apache-2.0.

## Current state: M00 contracts, not an application

This snapshot contains nine versioned design artifacts and the independently
authored acceptance tests. There is no installer binary, UI application, database
schema, connector, inference worker or proof executor yet. A successful build
does not produce an installable product.

Linux/Podman Quadlet and Kubernetes/Helm are the selected deployment targets.
Managed PostgreSQL and private shared S3 are the default design; AI and proof
are disabled. All eight native and four AI families are **planned and unverified**.
Capacity figures are hypotheses, not benchmark results.

### Read the contracts

`docs\contracts\v1alpha1` contains installation/provider schemas, safe resolved
examples, the installer contract, integration/provider catalogs, UX budgets,
capacity profiles and synthetic lifecycle cases. Schema IDs use `.invalid`
identifiers and tests forbid external schema loads.

- `contracts\CONTRACTS.txt`: executable contract and test-author handoff.
- `docs\adr\0001-m00-decisions.md`: identity, contribution, source/CI/registry and stack decisions.
- `docs\architecture.md`: service ownership, ingestion boundaries and lifecycle safety.
- `docs\dependencies.md`: license findings and distribution-review boundaries.
- `docs\design\m00-journeys.html`: original static storyboard, not an implemented UI.
- `docs\operator-research.md`: operator assumptions and bounded comparison decisions.
- `CONTRIBUTING.md`: contribution and independent-review rules.

**Animate UI candidate:** historical commit
`38b917762e3b6059c06a7af703071ba11a89091e` has a verified MIT root license,
resolving the M00 root-license blocker. Current main and its registry remain
unapproved. M01 must review selected files, notices and nested dependencies
before adoption; no Animate UI source has been copied. See `docs\dependencies.md`.

## Validate

The module requests Go **1.27.1** via `toolchain` and has a Go 1.27.0 language
minimum. From the repository root on Windows:

```powershell
.\contracts\Test-M00.ps1
```

That runner keeps Go caches and scratch work under `.cache` and restores its
environment. After the initial toolchain/module restore, tests need no network,
Node, Docker, Kubernetes context, external account or proprietary CI service.
Direct source checks, with appropriately configured local Go caches:

```powershell
go test -json -count=1 -mod=readonly .\contracts
go build -mod=readonly .\...
```

Do not execute the example installation configurations: their development
release, `.invalid` address and synthetic cluster context are placeholders.
Schema validity is not semantic preflight, deployment support or readiness.

M00 still requires independent review. The seven documentary acceptance records
in `contracts\testdata\m00-requirements.json` are deliberately not approved by
the coder. In particular, file-level dependency reviews, rendered prototypes, operator
validation and final support review remain separate from green JSON tests.
