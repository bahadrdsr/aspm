# M01 interface and build foundation

Status: implemented increment pending independent M01 acceptance. M00 was
accepted separately at `9692bcf526bc6a2cd31818c379cf124f17910cf0`.
This document does not approve its own source adaptations, rendered references
or deployment support.

## Actual UI and data boundary

`web\src` is a real React 19.2.8 application, compiled by Vite 8.2.2 and the
React plugin 6.1.1, with TypeScript 7.0.2. There are no runtime imports from
`tests`, no remote fonts and no exposed provider/backend secret configuration.
Owned icons are simple SVG geometry, not an unreviewed icon package.

The HTTP client accepts only the versioned Work, finding detail and native
catalog DTOs. It makes same-origin GET requests without bearer/provider headers,
validates payload fields, times out, and displays actual error responses.
Abort controllers and effect ownership prevent a late response from replacing
a newer/unmounted view. A permission failure discards prior displayed records.
Transient refresh failures label retained data as stale rather than claiming a
fresh result. No failure path substitutes gallery or test examples.

Work filters its loaded response by title/asset/owner and renders at most 50
rows per accessible page. Selection distinguishes the current page and retained
selected IDs. Sorting is explicit; no background polling reorders rows.
Finding inspection opens a native modal dialog with focus inside, literal
evidence text, distinct source/collection/import times and immediate Escape
focus restoration. A `#/work?finding=<id>` link can open detail without a
previous table. The dialog occupies the full small-screen viewport.

The integration catalog is driven by API items, not eight hard-coded success
cards. Support maturity, connection and verification are shown separately.
Synthetic, planned or unverified records never become ready-to-connect.
No credential-entry or connector mutation is implemented.

Assets and Reports explain their unavailable backend capabilities rather than
manufacturing inventory or metrics. Settings leads to the explicitly synthetic
gallery. Gallery state examples are isolated presentation content, never
operational evidence or fallback rows.

## Reviewed components and motion

The source approval in `web\tests\reviews\m01-source-approval.md` and its
coordinator-owned registry covers:

- shadcn Button at `f1dd9c6903d4d9030efe7bd61533db3fa21e68d8`.
- Animate UI Button and Slot at
  `38b917762e3b6059c06a7af703071ba11a89091e`.

`web\src\components\provenance.json` identifies each source path, normalized
SHA-256, upstream snapshot and independent review. Source bytes are not silently
rewritten. Alias resolution connects both upstream utility imports to one owned
`clsx`/`tailwind-merge` helper and connects the historical Slot path to its
approved local file. No current Animate UI registry is fetched.

TypeScript preserves JSX during its no-emit analysis; Vite's React plugin owns
the automatic JSX transform. This keeps the approved historical React imports
verbatim while retaining strict checking, unused-local/parameter checks and
the real JSX/browser harness. No source patch, diagnostic suppression or
replacement component is needed.

Animate Button is used only in normal button mode through an owned action
wrapper. Its polymorphic Slot is not exposed to arbitrary or untrusted children.
The documented upstream Slot invalid-child/loose-typing limitation remains
contained, not claimed to be fixed. Any changed source needs an actual patch,
updated local hash and independent review.

One Motion runtime backs interaction feedback, selection and dialog entry.
Reduced-motion handling covers Motion configuration, explicit scale/entry
choices, CSS transitions and copied controls. There are no continuous ornamental
animations or fabricated job percentages. Critical values are not tweened.
Focus restoration does not wait for exit animation.

Theme selection follows the OS until explicitly changed; only the non-secret
`aspm.theme` preference is stored. Storage failure is visible and the current
page remains usable. Light/dark colors, focus rings and responsive layouts are
owned tokens. WCAG conformance, enterprise browser timing and all composed
interaction/accessibility cases still need independent review.

## Dependency and notice audit

Runtime pins: React/React DOM 19.2.8, Motion/framer-motion/motion-dom 12.43.0,
motion-utils 12.39.0, tslib 2.8.1, Radix Slot 1.2.3 / compose-refs 1.1.2,
CVA 0.7.1, clsx 2.1.1 and tailwind-merge 3.6.0.
Tailwind and its Vite plugin are 4.3.3. Overrides keep the reviewed Motion/Radix
closure from drifting to other versions.

CVA's inspected package license is Apache-2.0, not MIT. The remaining reviewed
runtime closure is MIT except tslib's 0BSD. Radix compose-refs omits its archive
license file; the exact independently approved upstream MIT notice is retained
as `web\public\notices\radix-MIT.txt`, Git blob
`a18858fb7b014098cba85703e66609be66a26ef5`.

`npm run audit:licenses` checks exact manifest/lock agreement, installed package
versions and declared FOSS expressions, and requires installed runtime notices.
It generates `dependencies.txt`, `dependency-inventory.json` and `sbom.cdx.json`
in `web\public\notices`; the production build packages them. These generated
files are ignored in source control, while source-license snapshots and
generation code are retained.

The lock includes build tools and optional native packages for other platforms.
Only host-installed runtime notices are inspected by the local report. Metadata
alone is not a full per-file distribution audit, and some mirror-provided
archive records use SHA-1 integrity. The notice bundle adds SHA-256 content
digests but does not retroactively independently authenticate package origin.
Canonical endpoint TLS access was unavailable in this environment; exact pinned
archives were restored through the configured mirror with TLS verification and
system CAs enabled. No dependency version or default tag was substituted.

The mirror exposed archive URLs on a different host; npm's unconditional
registry-host replacement produced a 404. Using
`--replace-registry-host=never` preserved those exact download locations.
`scripts\normalize-lock.mjs` then rewrote only recognized archive URLs to their
canonical public locations without changing versions or integrities.
A clean offline `npm ci` using the canonical registry selection verifies cache/
lock portability without requiring the maintainer's particular package mirror.
No machine-wide TLS changes or disabled certificate checks are permitted.

## Development host

`cmd\aspm-dev` serves a built asset tree on literal loopback IPs only. The
filesystem root is bounded using `os.OpenRoot`; directories are not listed.
The host adds content-type, referrer and CSP protections. Inline styles remain
permitted for React/Motion; untrusted evidence is never interpreted as HTML.

`GET /healthz` reports **development-host-only**, not application readiness.
All `/api` paths return an explicit unavailable JSON envelope; writes are
rejected. There are no database connections, user identities, credentials or
sample data in the host. Its unit tests exercise these boundaries against a
controlled in-memory asset fixture. This is not the M02 service skeleton.

## Reproducible checks and artifacts

`scripts\check.mjs` runs config structure checks, lock checks, strict typecheck,
the unchanged browser suite, static build and Go tests/build. It keeps Go
toolchain/module/build/scratch paths project-local and requests Go 1.27.1.
No proprietary CI or container desktop is needed for local checks.

`scripts\build.mjs` recreates only `.artifacts\development`, builds with
`-trimpath -buildvcs=false`, copies UI/notices and Go's license, and records
source revision, dirty status, lock digest and every artifact digest. It does
not claim cross-platform byte identity, a verified base image or a published
release. Native package/OS differences remain part of reproducibility review.

`scripts\sign.mjs` uses Ed25519 through Node's FOSS crypto runtime. Signing needs
a clean checkout whose exact commit equals a protected reviewed-revision
approval, plus a protected private-key file reference. Verification uses an
independently trusted public-key file, not a key trusted merely because it came
with the artifact. Digest and signature mismatches fail explicitly. The
`self-test` command uses an in-memory synthetic key and tampered message;
it is not a real release-signing or publication result.

## CI and reference infrastructure are not deployed

The `.woodpecker` files use JSON syntax, which is also valid YAML.
Contribution checks receive no secrets, privileged mode or container socket.
The separate manual-main release workflow requires an isolated release worker,
a protected reviewed revision and a mounted signing-key file. The worker's
`infra\ci\Containerfile` pins Node/Go version tags; immutable image digests,
base-image contents and worker deployment still require review. The image named
in the release workflow is an operator-built prerequisite, not a published
image claimed to exist.

GitHub hosting does not select GitHub Actions or GHCR. The maintainer registry
reference is CNCF Distribution 3.0.0 with a persistent named volume in
`infra\ci\registry.container`. It binds loopback only, and is separate from the
customer application. Do not expose it externally without separately reviewed
TLS/authentication, resource limits and backup controls. No OCI push is run by
the coder or these build/sign scripts.

OpenTofu JSON in `infra\tofu\reference` declares three dedicated KVM/libvirt
reference VMs, a new NAT network and protected persistent volumes. Creation
defaults off. An operator must supply an independently reviewed local Ubuntu
24.04.5 image and its exact digest. Existing resource adoption is not automatic.
The example intentionally has an invalid placeholder digest; replace it in an
ignored local input file. Guest enrollment, K3s, CSI and ingress are not installed
by this skeleton.

The Ansible profile inspects declared Ubuntu/systemd/cgroup prerequisites and
existing Podman, without silently installing packages. Its only filesystem
change is the dedicated lab input directory behind an explicit opt-in and
privilege request. The example inventory uses documentation-only IP addresses.
Use protected SSH identities; do not commit private keys, state or local inputs.
OpenTofu local state needs operator-controlled encryption, permissions, locking
and recovery before real provisioning; no encryption guarantee is implied.

Native tools were not available in the Windows environment. On an authorized
reference host, before any apply, run OpenTofu 1.10.6 init/validate/plan against
libvirt provider 0.8.3 and Ansible Core 2.19.3 syntax/check mode with the selected
inventory. Review the generated provider lock and all intended changes.
`scripts\check-config.mjs` checks JSON/YAML structure, versions and safety gates,
**not** native provider semantics or a real deployment. No VM, Kubernetes
cluster, CI worker, registry, signed release or stateful HA verification is
reported as passed by M01.

## Review artifacts

`npm run test:capture` uses the real application and the existing independent
HTTP fixture boundary to write work, evidence and gallery light/dark screenshots
plus small-screen reduced-motion gallery views to `web\.artifacts\review`.
The manifest labels them synthetic and unapproved. No screenshot baseline is
automatically accepted. Independent M01 review must inspect the exact passing
revision, source adaptations, complete dependency distribution, responsive/
keyboard/focus behavior and rendered references before accepting the milestone.
