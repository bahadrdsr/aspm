# M07 compiled-in report adapters

Implemented on October 8, 2026.

ASPM report intake uses one bounded adapter contract in
`pkg/reportadapter`. The production registry is immutable after construction,
sorts descriptors by stable format ID, rejects duplicate IDs, and drives parser
dispatch plus the report-intake catalog returned by
`GET /api/v1/integrations/catalog`.

## Contributor contract

An adapter supplies:

- A stable lowercase format ID and human-readable name.
- `report-importer` or `manual-intake` kind.
- Exact admitted version or profile labels.
- Experimental or supported maturity.
- Field coverage and lifecycle capability declarations.
- `none` or `declarative-fields` mapping policy.
- One sanitized repository-relative deterministic fixture.
- Mapping validation and a parse function that returns normalized findings.

The shared boundary rejects empty, non-UTF-8, malformed, unsupported, and
reports larger than 32 MiB. It admits at most 5,000 findings and validates
stable identity, source identity, title, normalized severity, location and
bounded semantic text before findings can reach reconciliation.

Adapters are compiled into the application. Do not add uploaded Go plugins,
JavaScript, templates, shell commands, arbitrary property expressions, scanner
execution, or executable mapping scripts. Generic JSON and CSV mappings remain
literal field names only.

## Adding an adapter

1. Implement `reportadapter.Adapter` in a normal Go package.
2. Add a redacted synthetic supported-version fixture under that package's
   `testdata` directory.
3. Run `adaptertest.Run` from
   `pkg/reportadapter/adaptertest` with valid, unsupported-version, over-limit,
   malformed and semantic-field expectations.
4. Add the adapter constructor to `builtinAdapters` in
   `internal/parsers/adapters.go`. Parser dispatch, catalog serialization and
   shared bounds require no switch changes.
5. Add format-specific negative cases for ambiguous or unsafe source shapes.
6. Update operator documentation without claiming broad vendor-version or live
   connector certification.

`examples/reportadapter` is a complete synthetic example. It registers in a
new immutable registry and passes the reusable conformance suite without being
advertised as a production format.

## Built-in profiles

The current registry contains SARIF 2.1.0, Trivy SchemaVersion 2, OWASP ZAP
traditional JSON `@version` 2.16.1, Gitleaks v8-style JSON arrays, mapped JSON,
mapped UTF-8 comma-delimited CSV, and manual structured JSON.

These are exact admitted fixture profiles, not claims that every producer
release or output option is compatible. Report adapters do not count as native
integration families, do not establish source connection health, and do not
provide authorized live-vendor verification.
