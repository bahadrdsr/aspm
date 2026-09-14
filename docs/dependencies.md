# M00 dependency and distribution review

Review date: September 14, 2026. Status: **partial, pending independent review**.
No UI package, database driver, generator, CI service or runtime image is
installed into the product by this increment. Only the test dependency graph
already authored in `go.mod`/`go.sum` is used.

## Current source/test graph

| Item | Exact version | Top-level license evidence | Scope |
| --- | --- | --- | --- |
| Go | Toolchain 1.27.1; language minimum 1.27.0 | Go BSD-style license, `https://go.dev/LICENSE` | Toolchain selected by the existing module; no proprietary build dependency |
| `github.com/santhosh-tekuri/jsonschema/v6` | 6.0.2 | Apache-2.0, restored module `LICENSE` inspected locally | Test-only Draft 2020-12 validator; not a production import |
| `golang.org/x/text` | 0.14.0 | BSD-3-Clause, restored module `LICENSE` inspected locally | Indirect validator dependency; no new version selected by the coder |
| Original M00 HTML/CSS | This repository revision | Apache-2.0 | No copied component source, downloaded fonts, images or icons |

Local module evidence was read under `.cache\modules`; versions/checksums remain
in the test author's unchanged manifests. Preserve upstream license texts/notices
if distributing toolchains, vendored modules or compiled test artifacts.
This top-level review is not an SBOM or assurance about every file in a release.

## Animate UI candidate: pinned MIT root license

The M00 root-license blocker is resolved for historical candidate
`38b917762e3b6059c06a7af703071ba11a89091e`, not for current main.
The exact pinned `LICENSE.md` was read in full and its raw bytes verified on
September 14, 2026: standard **MIT**, Copyright (c) 2025 Elliot Sutton, with
the normal sale/redistribution grant and notice requirements, and no Commons
Clause. This preserves the requested Animate UI plus shadcn/ui, Motion and
Radix design direction without adopting restricted current source.

| Evidence | Immutable identifier |
| --- | --- |
| Candidate source commit | `38b917762e3b6059c06a7af703071ba11a89091e` |
| Pinned `LICENSE.md` Git blob | `0ca4a48bd712fd8db31fff20c8b241c5cf41e4db` |
| Pinned license SHA-256 | `d2349753738223c0237d8b5908c4e4da825bbfc951e4fa7081ebc5563c8ceea0` |
| First constraint-adding commit | `5e4209180bda6e3accc4d3dcc820a135f6357cfa` |

Primary history confirms that the candidate is the sole parent of the
constraint-adding commit, authored and committed September 16, 2025 at
15:22:25 UTC. Verification recomputed the Git blob ID from the exact downloaded
license bytes, rather than relying on a badge or whitespace-sensitive excerpt.

```text
https://raw.githubusercontent.com/imskyleen/animate-ui/38b917762e3b6059c06a7af703071ba11a89091e/LICENSE.md
https://github.com/imskyleen/animate-ui/commit/5e4209180bda6e3accc4d3dcc820a135f6357cfa
```

Current main remains **unapproved**: the previously inspected license blob
`4c99063f507027c06af430d54c5b3cf167be51d7` contains **MIT + Commons Clause
License Condition**. A historical MIT grant does not relabel current main.
No Animate UI source files have been adopted or copied.

The root-license finding is a candidate-selection decision, not complete
distribution approval or a legal compatibility opinion. M01 must review each
selected Radix component, its provenance/notices and all transitive imports and
dependencies against the pinned tree. **Do not fetch the current Animate UI
registry**, including nested `registryDependencies` referenced by historical
files. Resolve reviewed references to pinned local files and separately locked,
reviewed package dependencies before copying; reject unreviewed remote
resolution. Component compatibility, modifications, accessibility and the
complete distribution still require independent review.

## Selected design/build candidates, not installed dependencies

| Component | Candidate baseline | Decision / review state |
| --- | --- | --- |
| React / React DOM | 19.1.1 / 19.1.1 | React/Vite SPA, no production Node server; exact package graph not adopted |
| TypeScript | 5.9.2 | Build-time type checking; package verification pending |
| Vite | 7.1.5 | Static bundle; exact plugin/transitive graph and suitability review pending |
| Tailwind CSS | 4.1.13 | Owned tokens and static CSS; package graph pending |
| Motion | 12.23.12 | One motion system; upstream source license inspected as MIT; package graph pending |
| Radix primitives | `radix-ui` 1.4.3 | One primitive family; source license inspected as MIT; individual primitive versions require a lockfile |
| shadcn/ui | Reviewed Radix subset, immutable revision still required | Current source license inspected as MIT; no source copied |
| Animate UI | `38b917762e3b6059c06a7af703071ba11a89091e` | Root MIT verified; file/dependency review remains for M01; no source copied; current main/registry unapproved |
| Node | Maintainer environment 22.20.0 | Build tooling only, not a product runtime requirement |
| pgx | v5.7.5 | Selected transport/pool direction, MIT at this tag; no module added |
| sqlc | v1.29.0 | Selected build-time generator, MIT at this tag; no tool installed |

Version numbers above are bounded review candidates, not a lockfile, current
recommended patch set or a tested compatible composition. The earlier coder
registry probe failed on certificate trust and a local npm invocation; the
coordinator confirms `npm.cmd` works on Windows. The pinned GitHub license/history
check succeeded with `NODE_USE_SYSTEM_CA=1` and TLS verification enabled.
Use that system-trust option when needed; never disable certificate verification.
This correction makes no registry requests or package installations.

Exact package integrity, transitive versions, other UI source commits and
distribution notices remain a blocking review item before component adoption.
Do not ship candidates merely because this table names them or their root
license is compatible.

Source references for the top-level files actually inspected:

```text
https://raw.githubusercontent.com/shadcn-ui/ui/main/LICENSE.md
https://raw.githubusercontent.com/imskyleen/animate-ui/main/LICENSE.md
https://raw.githubusercontent.com/imskyleen/animate-ui/38b917762e3b6059c06a7af703071ba11a89091e/LICENSE.md
https://raw.githubusercontent.com/motiondivision/motion/main/LICENSE.md
https://raw.githubusercontent.com/radix-ui/primitives/main/LICENSE
https://raw.githubusercontent.com/jackc/pgx/v5.7.5/LICENSE
https://raw.githubusercontent.com/sqlc-dev/sqlc/v1.29.0/LICENSE
```

Moving-branch references document inspection, not immutable component adoption.
The copied-source manifest must eventually identify each origin path, commit,
content digest, license, local modifications and dependency notices. No paid UI
kit, proprietary font or unreviewed component registry is approved.

## Runtime and maintainer infrastructure

- Managed storage uses the coordinator-selected PostgreSQL `18.6-alpine` image
  tag and SeaweedFS community `4.47` digest from `install.defaults.json`.
  The PostgreSQL platform digest, image contents, OS packages, transitive
  notices and SBOM still require release review. Image availability is not
  multipart/checksum/access/isolation/recovery verification.
- SeaweedFS must stay community-only, persistent and private, with telemetry,
  admin UI, WebDAV, Iceberg and Lance disabled explicitly. Upstream management
  services remain internal even after UI disablement.
- Woodpecker and CNCF Distribution are separately chosen FOSS maintainer
  services. Distribution 3.0.0's top-level Apache-2.0 license and Woodpecker's
  current Apache-2.0 license were inspected; complete deployment image locks,
  secrets, patch suitability and distribution audits are not complete.
- Podman, Helm, Caddy, K3s, Kubernetes, OpenTofu, Ansible and the reference
  infrastructure remain selected architecture tooling, not a bundled M00
  distribution. Freeze exact versions and inventories before deployment.
  GPL, AGPL and MPL are not automatically non-FOSS; compatibility obligations
  depend on how actual components are combined and distributed.
- Hosted OpenAI, Foundry and Anthropic services are explicitly optional,
  customer-approved external services, **not** FOSS or local inference.
  No local model/runtime bundle is approved. Review runtime and the entire
  model system separately; open weights alone do not establish openness.

Before distribution, inventory direct/transitive code, copied UI, icons/fonts,
tools, containers/base OS, infrastructure providers and bundled executables;
retain notices, produce an SBOM, review combination/redistribution obligations,
and record independent approval against exact immutable artifacts.
