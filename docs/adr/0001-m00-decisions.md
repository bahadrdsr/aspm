# ADR 0001: M00 product and implementation boundaries

Date: September 14, 2026.
Status: coder decision record, pending independent review. This is not milestone
approval or evidence that any selected service has been deployed.

## Identity, licensing and stewardship

- Working name: `aspm`. Planned CLI: `aspmctl`.
- Canonical source destination: `github.com/bahadrdsr/aspm`; Go module:
  `github.com/bahadrdsr/aspm`.
- Original application, installer, adapters, SDKs, docs and fixtures:
  **Apache-2.0**, with contributor copyright retained.
- Contribution policy: inbound equals outbound, DCO 1.1 sign-off, no CLA or
  copyright assignment; see `CONTRIBUTING.md`.
- Initial maintainer/release authority: `bahadrdsr`. Intended access is public
  source with maintainer-controlled writes, reviewed pull requests, protected
  release credentials and no author self-approval. These are desired controls,
  not a claim that GitHub rules have been configured.
- Funding may support maintenance/sponsored work; no proprietary enterprise
  edition, license server or contractual support SLA is selected.

Repository creation, visibility changes, rules, commits and publication belong
to the coordinator. This M00 coder increment does not perform them.

## Independent source, build and distribution decisions

Select **Woodpecker CI** for the future maintainer build/release environment,
using its documented GitHub integration. Select **CNCF Distribution Registry
3.0.0** as the candidate FOSS OCI registry baseline, independent of GitHub.
Do not assume GitHub Actions, GitHub Container Registry or a source-code forge.
These are component choices, not a production-deployment approval of those
versions. Freeze image digests, upgrades and complete inventories at M01
before provisioning; re-review security/maintenance suitability then.

Maintain separate resource ownership:

| Environment | Intended responsibilities | Not a customer prerequisite |
| --- | --- | --- |
| GitHub source host | Source, issues/PRs and review metadata | A running application needs no project-host credentials |
| Woodpecker workers | Reproducible tests, builds, SBOM and release stages | No installed CI platform at the customer |
| Distribution registry | Immutable OCI blobs and signed release artifact discovery | Customer can supply an approved mirror or signed bundle |
| Customer installation | API, independently deployed workers, PostgreSQL and S3 | No maintainer network, proprietary CI or Docker Desktop |

An ordinary FOSS source build uses the recorded Go toolchain and test dependency
graph with `go test -mod=readonly` and `go build -mod=readonly`. That path already
checks M00 without CI. The later UI build uses a pinned Node/npm toolchain and
lockfile, followed by Go-served static assets; Node is not a production service.
No hosted signing identity service is mandatory: select operator-owned signing
keys with auditable public verification material. Signing/registry/CI
configuration and offline release bundles are M01/M13 work, not shipped here.

Sources inspected for these choices:
`https://woodpecker-ci.org/docs/administration/configuration/forges/github`,
`https://raw.githubusercontent.com/woodpecker-ci/woodpecker/main/LICENSE`,
`https://distribution.github.io/distribution/about/`,
`https://raw.githubusercontent.com/distribution/distribution/v3.0.0/LICENSE`.
Their top-level licenses do not establish a complete release-image audit.

## Backend, ownership and storage

Select Go 1.27.1, standard HTTP handlers and explicit module boundaries, not a
single process with every workload competing for the API's heap.

Select **pgx v5** for PostgreSQL transport and bounded pools, with
**sqlc** as a build-time typed-SQL generator for stable application queries.
Candidate review versions are pgx 5.7.5 and sqlc 1.29.0, not installed or locked
runtime dependencies. Direct SQL remains the source of truth; use pgx's
transaction/COPY support where generation is inappropriate. Avoid an ORM that
hides transaction, batching or query-plan control. Exact transitive adoption
and compatibility remain blocking until reviewed; see `docs\dependencies.md`.

Use PostgreSQL for metadata, jobs, receipts and projections. Do not add a broker,
graph database or search server without measured necessity. Ingestion parsing
and reconciliation are separate processes/deployments from day one of M02.
AI and controlled proof get independent execution pools when their milestones
arrive. Human decisions remain core-owned.

Use shared S3-compatible evidence storage, with the community SeaweedFS 4.47
image/digest frozen in the installation contract. The coordinator selected
`postgres:18.6-alpine` and
`chrislusf/seaweedfs:4.47@sha256:ce9e796f1fe6f06968f4c04bdaf8f678dad9c8acdfef3d244133d71bfa6bf882`
using local image checks. That is not this coder's deployment/conformance
evidence. PostgreSQL's platform digest must still enter the signed release lock.
No enterprise-only object-store feature or telemetry is a runtime requirement.

## Deployments

Select an existing Ubuntu Server 24.04.5 amd64 host with Podman/systemd Quadlet,
or an existing Kubernetes 1.35.8/1.36.4 cluster with an owned Helm chart.
These explicit candidate versions are **not-run, not production-ready** in
`installer.contract.json`. Exact prerequisite package/ingress/CSI and K3s lab
locks need review before deployment testing. KVM/libvirt plus K3s is the
reference lab, not an obligatory customer hypervisor.

OpenTofu owns reference VM/network/disk provisioning; Ansible owns declared
host preparation and Quadlet files; Helm owns namespaced application resources.
The Go installer validates/plans/approves and invokes those owners, without
another hidden shell-based deployment engine. Do not replace cluster-wide
networking. Default persistent data survives reapply, resume and uninstall.

The Linux and Kubernetes configurations share service topology, secret-reference
semantics, permissions, retention and connection limits. Process isolation is
not added host capacity; independent replicas are not stateful HA. Offline
inputs are modeled, but disconnected installation is unverified.

Platform sources: `https://releases.ubuntu.com/24.04/`,
`https://kubernetes.io/releases/`,
`https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html`.

## Design direction and pinned Animate UI candidate

Preserve React, TypeScript, Vite, Tailwind, shadcn/ui, Animate UI and one Motion
system as the requested design direction. Select **Radix** variants consistently
for foundational and animated primitives. Do not casually mix Base UI.

Select historical Animate UI commit
`38b917762e3b6059c06a7af703071ba11a89091e` as the M00 source candidate.
Its verified root `LICENSE.md` is standard MIT, with Git blob
`0ca4a48bd712fd8db31fff20c8b241c5cf41e4db`. It is the parent of
`5e4209180bda6e3accc4d3dcc820a135f6357cfa`, which added the license constraint
on September 16, 2025. This resolves the root-license blocker while preserving
the FOSS plus Animate UI requirement; no library substitution is needed.

Current main remains **MIT + Commons Clause and unapproved**. The candidate
does not approve newer source or constitute completed adoption. **No Animate UI
source files are copied in M00.** M01 must review each selected Radix file and
all transitive dependencies, retain provenance/notices, and test the resulting
composition. Never fetch the current Animate UI registry or follow unreviewed
nested registry references, even when a historical file contains them. Stage
reviewed pinned local sources and locked dependencies instead.
`docs\dependencies.md` records the immutable license/history evidence and the
remaining component/distribution review.

`docs\design\m00-journeys.html` is original static HTML/CSS showing install,
source setup, finding evidence, action preview and light/dark state treatments.
It loads no external assets and makes no outbound requests. It is not a React
scaffold, installed component library or passed WCAG/visual acceptance.
`ux.contract.json` freezes interaction/motion/measurement hypotheses; independent
rendered review remains required.

## Support and review gate

All eight native integration families and four AI families have concrete
planned profiles, capability boundaries, permissions and verification plans.
No profile is implemented, ready or live-verified. A real account used for
development does not verify the application's connector.

M00 defaults keep AI/proof disabled and require no source accounts. Verification
stays deterministic evidence or controlled fixtures; arbitrary shell/browser
execution and autonomous offense are excluded. Source observations, inferred
resolution, human disposition and proof outcome remain separate axes.

The test author's seven manual acceptance records remain pending. This record
does not approve its own license analysis, rendered UX, operator assumptions
or support matrix. M00 cannot be marked complete until independent review,
blocking decisions and final exact-revision checks are recorded.
