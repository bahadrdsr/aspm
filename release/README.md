# Technical preview release inputs

`technical-preview.json` is the reviewed release input for `0.1.0-rc.1`.
It pins the Linux/amd64 target, PostgreSQL and SeaweedFS images, and every
Containerfile base-image index digest.

From a clean reviewed checkout:

```powershell
node scripts\release.mjs prepare
$env:ASPM_RELEASE_APPROVED_REVISION = git rev-parse HEAD
$env:ASPM_SIGNING_KEY_FILE = 'C:\protected\aspm-release-ed25519.pem'
node scripts\release.mjs finalize
node scripts\release.mjs verify --trusted-key C:\trusted\aspm-release-public.pem
```

The signing key must be supplied outside the repository. Verification rejects
a trust key read from inside the generated release directory. The included
`release-public-key.pem` is distribution material only and cannot establish
trust by itself.

The output under `.artifacts\release\0.1.0-rc.1` contains:

- a Linux/amd64 binary and web payload archive;
- an OCI image-layout archive with the exact application image digest;
- a signed installer-bundle archive;
- a CycloneDX 1.6 SBOM;
- an in-toto/SLSA provenance statement;
- exact SHA-256 sums;
- a signed canonical release manifest.

The release-specific Containerfile packages the already verified Linux binaries
and `web/dist` output. This avoids dependency downloads and recompilation inside
BuildKit while keeping the locked builds and OCI packaging as separately
checked stages.

No script pushes an image, creates a forge release, or changes a registry.
Before deployment, load or publish the OCI archive under the exact
`localhost/aspm:0.1.0-rc.1@sha256:...` identity recorded in the release
manifest.
