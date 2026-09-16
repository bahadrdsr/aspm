# Contributing to aspm

## License and provenance

Original contributions, including adapters, SDKs, documentation and synthetic
fixtures, use Apache-2.0, the same license as the project. Contributors retain
their copyright. No copyright assignment or additional CLA is required.

Use the Developer Certificate of Origin (DCO) 1.1 and add your own
`Signed-off-by: Name <email>` to each contribution commit. Sign only when you
can make that certification. Do not sign on another person's behalf or treat
an AI-generated attribution as a human certification. DCO text:
`https://developercertificate.org/`.

Disclose copied/generated code and AI assistance. Record the exact source
revision, license and modifications for third-party code; preserve notices.
An SPDX label or upstream README badge alone is not a complete license review.
Never strip restrictions to make an otherwise incompatible dependency appear
Apache-2.0. Review direct/transitive code, tools, images, fonts and icons before
adoption. Runtime and model-system reviews are separate for local AI.

## Scope and workflow

Use a small, demonstrable milestone increment with its acceptance criteria.
The roles are independent:

1. Test author defines fixtures/gates and demonstrates meaningful failure.
2. Coder implements the bounded feature and relevant documentation.
3. Independent reviewer challenges correctness, safety, performance, UX,
   accessibility, licenses and test adequacy.
4. Coder fixes implementation defects; test author owns expectation corrections.
   Rerun the required checks on the exact reviewed revision before acceptance.

Do not delete, skip or weaken acceptance tests to obtain green results.
Changes to contracts, budgets or visual references need a recorded requirement
rationale and test-author/reviewer approval. Schema fixtures are not proof of
an implemented connector or lifecycle engine.

For M00, run `.\contracts\Test-M00.ps1` and the readonly Go test/build commands
in `README.md`. No production dependencies should be introduced to satisfy a
documentation-only gate. Future hot-path changes also require the relevant
controlled replay/load checks, not an unrelated full test run.

## Data, integrations and verification

- Submit deterministic synthetic data or permissioned, properly sanitized
  fixtures. Never include credentials, private customer reports or personal
  data in source, screenshots, test output or a public issue.
- Keep report/scan/import-attempt identity distinct. Preserve original
  evidence, source severity, scope, notes and human decisions.
- Record unsupported fields, editions and capabilities explicitly. A mock
  cannot justify a live-verification claim.
- Real-endpoint checks require authorization and synthetic or explicitly
  approved data. Do not run these as untrusted contribution checks.
- Active verification is limited to deterministic evidence and controlled
  fixtures. No exploits, arbitrary shell/browser execution, model-controlled
  commands or autonomous offensive workflows.
- Report suspected sensitive defects privately to the maintainer through an
  available private channel. Do not post credentials or attack payloads; no
  private-reporting endpoint or response-time promise is claimed in M00.

## Review and access

`bahadrdsr` is the initial maintainer and release authority. Contributions use
pull requests and independent review; authors do not self-approve. Changes to
schema/data identity, licenses, release signing and destructive operations
need explicit maintainer review. Repository rules and credential separation
are configuration work owned by the coordinator, not controls already deployed.

Untrusted contribution builds must not receive signing keys, registry write
tokens, cloud credentials or production network access. Paid support or
sponsorship does not establish a separate proprietary product edition or
override technical acceptance gates.
