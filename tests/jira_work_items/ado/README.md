# Azure DevOps application acceptance

These files define the implemented selected Azure DevOps Services
repository/build-artifact/SARIF vertical. On October 7, 2026, A1-A5 passed
together against fresh owned PostgreSQL, S3, native TLS and pinned published
V11 fixtures.

Read `CONTRACT.txt` and `OLD_OBSERVER_PROPOSAL.txt` before editing them.
The related `ado_*_test.go` files use `integration && ado_collection`.
Normal tests without `ado_collection` do not select these scenarios.

## Required private prerequisites

- `ASPM_ADO_CAPABILITIES` needs four real, distinct, explicitly granted local
  storage identities. Grants must cover the actual owned collection/raw test
  prefixes. Do not substitute an all-powerful shared key or fake S3 success.
- The published-V11 fixture must be built from its pinned Git objects, with
  its generated receipt supplied explicitly. Do not relabel current code as
  the old database producer.
- The V12 current-observer proposal is approved and applied only to current
  compatibility observers. Historical migration literals, fixtures and signed
  evidence remain unchanged.
- `tests\internal\sourcecompat` has independent positive and rejection coverage
  for the exact catalog and complete-row projection.

The acceptance uses an owned synthetic Azure DevOps protocol server. It does
not certify a real organization, PAT scope, project/repository permission or
vendor availability. Live-account qualification remains explicit operator work.

See `docs\m09-azure-devops.md` and the M09 UAT module for the implementation,
proof boundaries and remaining work. Other unfinished integrations remain
deferred while the roadmap returns to M06.
