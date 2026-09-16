# M01 independent source approval

Authority: implementation coordinator, separate from the production coder.
Date: September 14, 2026.
Decision: the three exact sources in `approved-component-sources.json` are
approved for bounded adoption with the conditions below. This is an upstream
source/import-license review, not approval of unwritten application changes.

## Sources and applicable licenses

- shadcn Button: commit `f1dd9c6903d4d9030efe7bd61533db3fa21e68d8`,
  `apps/v4/registry/new-york-v4/ui/button.tsx`.
- Animate UI Button and its Slot dependency: commit
  `38b917762e3b6059c06a7af703071ba11a89091e`,
  `apps/www/registry/primitives/buttons/button/index.tsx` and
  `apps/www/registry/primitives/animate/slot/index.tsx`.

The full contents of these files were inspected. No file-specific alternative
license, commercial restriction, dynamic registry fetch, network operation or
code loader was found in this selected source closure. The historical Animate
UI app/workspace package manifests are private npm packages without an alternate
license declaration; private publishing status is not an additional license.

The exact source and root-license bytes were retrieved through the publisher's
GitHub API at the immutable revisions. Test snapshots and the approval registry
bind normalized source SHA-256, complete license SHA-256 and exact Git blob IDs.
The shadcn license grants standard MIT permissions with its 2023 copyright
notice. The selected Animate UI license grants standard MIT permissions with
the 2025 Elliot Sutton notice. Current Animate UI main/registry is NOT approved.

## Reviewed import closure

The shadcn Button imports React, `@radix-ui/react-slot`,
`class-variance-authority`, and the application's shared `cn` helper. Animate
Button imports React, Motion and the selected Slot; Slot imports React, Motion
and the same class-name helper through an upstream workspace alias.

Use a small owned `cn` helper built from `clsx` and `tailwind-merge`. Map the
upstream local/workspace import paths to that helper and to the approved Slot,
or expose any actual source edits as a reviewable patch. Do not fetch transitive
component dependencies from the current Animate UI registry.

Runtime dependency evidence inspected for this adoption:

| Dependency | Inspected version | License evidence |
|---|---|---|
| React / React DOM | 19.2.8, existing test lock | MIT package/source metadata and existing locked graph |
| `@radix-ui/react-slot` | 1.2.3 | Full MIT text in its integrity-bound npm archive |
| `@radix-ui/react-compose-refs` | 1.1.2 | Package declares MIT; archive omits a license file, so retain the upstream Radix MIT notice |
| `class-variance-authority` | 0.7.1 | Full Apache-2.0 text in its npm archive |
| `clsx` | 2.1.1 | Full MIT text in its npm archive |
| `tailwind-merge` | 3.6.0 | Full MIT text in its npm archive |
| `motion` / `framer-motion` | 12.43.0 | Full MIT texts; inspected dependency declarations |
| `motion-dom` | 12.43.0 | Full MIT text; depends on Motion utilities |
| `motion-utils` | 12.39.0 | Full MIT text; no runtime dependencies in inspected manifest |
| `tslib` | 2.8.1 | Full 0BSD permission text |

Package archives were obtained without executing package scripts, with normal
TLS verification and integrity metadata. This establishes the selected import
closure's FOSS direction, not an assurance that every later lockfile entry or
release-image file has already been audited. The coder must pin the resolved
graph, preserve applicable notices, and surface extra dependencies for M01
review rather than infer approval for arbitrary versions/packages.

Radix upstream root MIT license evidence:
`https://github.com/radix-ui/primitives/blob/main/LICENSE`, Git blob
`a18858fb7b014098cba85703e66609be66a26ef5`. The package's omitted notice must not
be interpreted as permission to omit attribution from our distribution.

## Usage and code-review conditions

- Retain source/license snapshots and required notices in the repository.
- Initial Animate Button usage should use its normal button mode. Do not pass
  untrusted values or invalid React children into the polymorphic Slot.
- Slot's upstream implementation reads child type before its validity guard and
  contains loose internal typing. This is a known integration limitation, not
  a claim that upstream source is defect-free. Any hardening/adaptation must
  have a reviewable diff and independent M01 code review.
- Verbatim adoption with deliberate alias resolution is acceptable. A changed
  source file must not be labeled verbatim or assigned the upstream hash.
- Actual use of both providers and the shared Motion runtime must be demonstrated
  in the production bundle; parked/unused copies do not count.
- Review browser behavior, reduced motion, focus, accessibility and production
  dependency output after integration. Source approval does not waive those
  gates or approve a screenshot baseline.
