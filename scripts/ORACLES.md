# Protected test oracles

`oracle-lib.mjs` exports synchronous `verifyOracle(options)`, `treeDigest({root,
files})`, and `checkBinding(source, path)`. `oracles.mjs` supplies explicit CLI
commands. There is no bundled authority, generated-key fallback, `--update`,
automatic recapture, or overwrite mode.

## Independent authority and CI

A trusted runner must provision an Ed25519 public-key **file**, its independently
approved SHA-256 SPKI-DER fingerprint, and a pinned oracle-set configuration.
Signing keys belong to the independent test author, not the implementation job.
Never commit private keys or pass their contents on the command line.

Woodpecker requires these runner-supplied values; missing values fail the job:

- `ASPM_ORACLE_CHECKER`: absolute path to `oracles.mjs` from a protected revision,
  with its corresponding protected `oracle-lib.mjs`.
- `ASPM_ORACLE_CONFIG`: oracle-set JSON file.
- `ASPM_ORACLE_CONFIG_SHA256`: `sha256:` plus the independently pinned exact
  configuration-file digest.
- `ASPM_ORACLE_TRUSTED_KEY_FILE`: independently supplied Ed25519 public PEM.
- `ASPM_ORACLE_TRUSTED_FINGERPRINT`: `sha256:` plus its SPKI-DER digest.

The runner must enforce this policy outside the proposed patch, including which
checker, workflow, authority, and configuration are approved. A checker, key,
fingerprint or workflow replaced by the same untrusted patch cannot certify that
patch. No verification-step private-secret binding was added.

`scripts/check.mjs` also requires the authority/configuration environment. CI
wraps it with the independently protected checker. Each guarded command verifies
oracles before and after execution, including on command failure, and compares
exact source/oracle digests. The before snapshot stays in the parent process,
not an editable workspace checkpoint. Failed commands never emit successful
acceptance.

Use an isolated, immutable checkout and read-only protected inputs in the trusted
runner. These checks do **not** turn shared-host tool permissions into an OS
sandbox, prevent arbitrary process actions, or detect every transient mutation
that an adversarial process restores before the final snapshot.

## Signed scope

Manifest schema 1 records `scope`, explicit `phase`, `author`, `capturedAt`,
`redEvidence`, `protectedRoots`, `bindingPaths`, and exact `files` entries.
Each entry has `path`, `normalization`, and `sha256`. Signature verification
authenticates the manifest's exact bytes before any declared path is inspected.
Missing/changed protected files, unlisted files under protected roots, symlinks,
hard links, nonregular inputs, and escaping/aliased paths are rejected.

`none` hashes exact bytes. Signed `lf` converts only CRLF to LF at the byte level;
it does not remove BOMs, rewrite lone CR, trim whitespace, or transcode text.
Use `none` for binary/exact-byte fixtures and compiler/guard controls.
`treeDigest` always hashes exact bytes and framed, sorted relative file names.

Forwarding exceptions are only explicitly declared Go binding paths. The lint
rejects obvious test suppression, build directives, and substitute SQL, file,
network, HTTP-response or timer logic. It is a heuristic, **not independent
source review**. Tagged existing bindings should be fully frozen as ordinary
manifest files, not granted a mutable binding exception. Review every exception.

The trusted oracle-set JSON has this shape (paths are relative to the source root):

```json
{
  "schemaVersion": 1,
  "oracles": [{"manifest": ".oracles/m03-v1.json", "signature": ".oracles/m03-v1.json.sig"}],
  "requiredControls": ["scripts/oracle-lib.mjs", "scripts/oracles.mjs", "scripts/check.mjs"]
}
```

Every required control must also be protected by a signed manifest. Guarded
runs additionally require coverage of source-scope test files, test/helper and
fixture directories, scripts, CI files, dependency locks, and recognized compiler
configuration. The author must enumerate other imported helpers, compiler flags,
and guard inputs in `requiredControls`; the filename heuristic is not an import
graph proof. Compiler flags and test execution commands remain unchanged.

## CLI usage

Run from the module root. JSON file lists are explicit arrays of portable paths,
for example `["tests/example/contract_test.go","go.mod"]`; capture entries may
instead be `{"path":"tests/example/fixture.bin","normalization":"none"}`.
Protected-root and binding lists are separate arrays; no binding exceptions is
`[]`. These are inputs for the author to prepare, not generated baselines.

```powershell
node scripts\oracles.mjs fingerprint --trusted-key C:\oracle-authority\public.pem
node scripts\oracles.mjs capture --root . --manifest .oracles\m03-v1.json --signature .oracles\m03-v1.json.sig --signing-key C:\oracle-authority\author-private.pem --scope m03 --author independent-test-author --phase pre-code-red --files oracle-files.json --protected-roots oracle-roots.json --bindings oracle-bindings.json --red-log oracle-red.log --red-exit-code 1
node scripts\oracles.mjs verify --root . --manifest .oracles\m03-v1.json --signature .oracles\m03-v1.json.sig --trusted-key C:\oracle-authority\public.pem --trusted-fingerprint sha256:APPROVED_SPKI_DIGEST
node scripts\oracles.mjs digest --root . --files reviewed-tree-files.json
node scripts\oracles.mjs check --root . --config oracle-set.json --config-digest sha256:APPROVED_CONFIG_DIGEST --trusted-key C:\oracle-authority\public.pem --trusted-fingerprint sha256:APPROVED_SPKI_DIGEST --git -- node --test scripts\tests\oracles.test.mjs
```

`seal` aliases `capture`; both create new manifest/signature paths exclusively.
`pre-code-red` requires the real prior failing-log digest and nonzero exit code.
Use explicit `--phase reviewed-baseline` for recovered existing tests; omit RED
arguments when no genuine prior RED evidence exists. Neither mode proves history
or author independence merely from a supplied label. The signing-author process
and independent review must establish those facts.

`digest`/`check` accept exactly one of `--files` and `--git`. The latter includes
tracked and untracked files while excluding ignored generated artifacts; declared
oracle files and required controls cannot be omitted. `check-env` uses this Git
scope with the mandatory runner environment. Use `--files` outside a Git checkout.

Reviewer acceptance must cite both the `treeDigest` and `oracleDigest` from the
successful `tested-tree-verified` record, alongside the reviewed scope, authority,
and command evidence. Focused guard tests are not application-suite acceptance.

## Published history and clean checkouts

The `.oracles` directory contains recorded, signed handoffs from development.
They are not interchangeable: later, explicitly authorized test-author amendments
can supersede an older manifest's active file paths without changing its signed
bytes. Keep the original manifest, signature and archived source evidence as
history. A current runner must select the intended current control set explicitly;
verifying every historical manifest against today's worktree is not a valid gate.

Some development handoffs include installed Windows-specific TypeScript
declarations. Their recorded hashes are evidence for that environment, not a
portable dependency installation instruction. Restore dependencies from the
locked manifests and use the independently approved control set for the selected
runner platform. Protected production CI provisioning remains separate work.

Git preserves exact repository bytes rather than normalizing line endings.
This matters for signed controls, report fixtures and retained upstream sources.
Generated `.run`, `.security-run`, `.role-run`, dependency and build caches are
ignored. Only explicitly reviewed immutable evidence files within those ignored
locations belong in a commit; never add an entire generated directory by force.
Ignore rules do not replace inspecting the selected publication contents for
private credentials or other sensitive data.
