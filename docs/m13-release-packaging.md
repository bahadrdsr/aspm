# M13 technical preview release packaging

Implemented on October 9, 2026.

The release workflow produces one versioned Linux/amd64 technical preview from
a clean, explicitly approved Git revision. It does not publish to a registry or
forge automatically.

## Artifacts

`node scripts\release.mjs prepare` builds:

- all independently runnable ASPM binaries with `CGO_ENABLED=0`;
- `aspmctl` with the selected release version injected at link time;
- the production web application and dependency notices on the reviewed host;
- an OCI image-layout archive through Docker Buildx;
- a signed-installer-bundle staging tree using the exact OCI digest;
- a merged Go/npm CycloneDX component inventory.

`node scripts\release.mjs finalize` requires the exact reviewed Git revision and
an external owner-protected Ed25519 private key. It creates deterministic
tarballs, signs the installer manifest, writes in-toto/SLSA provenance,
generates SHA-256 sums, writes a canonical release manifest, and signs that
manifest.

`node scripts\release.mjs verify --trusted-key <absolute-path>` verifies:

- the independently supplied Ed25519 public key is outside the release output;
- the release-manifest signature and every declared file digest/size;
- exact SHA-256 sums;
- required binary/web payload members;
- installer-manifest signature, file digests, release and pinned image identity;
- the OCI index contains the declared application digest;
- CycloneDX and provenance identities bind the reviewed source revision.

The public key copied into the release is for distribution convenience only.
Trust must be established independently.

## Reproducibility boundary

Release tar members are sorted with fixed ownership, modes and timestamps, and
gzip metadata is platform-neutral. Go builds use `-trimpath`,
`-buildvcs=false`, locked modules, Linux/amd64 and disabled CGO. The OCI build
uses the already verified Linux binaries and `web/dist`, one digest-pinned
runtime base image, the source commit timestamp, Buildx provenance and timestamp
rewriting. It does not redownload dependencies or recompile source in a second
environment. The release SBOM is generated from the locked Go/npm graphs.

This establishes deterministic packaging inputs and exact output digests for
the reviewed build. It does not claim that unrelated Docker/BuildKit versions
produce byte-identical OCI archives. Tool versions are recorded in provenance
and the build receipt.

## Publication boundary

The manual Woodpecker workflow requires:

- a protected reviewed source revision;
- a protected Ed25519 private-key file;
- an independently supplied trusted public-key file;
- Docker Buildx on the isolated release worker.

The workflow performs no `docker push`, OCI registry upload, Git tag, or forge
release operation. Those remain an explicit maintainer publication step after
verification.
