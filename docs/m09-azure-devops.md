# Azure DevOps Services selected report collection

Implemented on October 7, 2026. This is a selected Azure DevOps Services
repository/build-artifact profile, not organization discovery, Azure DevOps
Server, Boards, pipeline triggering or an Entra authorization flow.

## Supported path

An administrator configures:

- Profile `ado-services-build-artifacts`.
- One organization name and canonical project/repository UUIDs.
- An explicit short-lived PAT, stored encrypted and never returned.
- An enabled state. Saving performs no provider request.

An analyst or administrator explicitly queues one canonical positive build ID,
one exact artifact name and one relative report path. The independent
collection worker performs four bounded read-only REST 7.1 requests:
repository, build, named artifact metadata and the exact ZIP download. It reads
one exact regular archive entry without extracting files to disk.

The durable collection preserves repository, pipeline, artifact and report
records with exact evidence bytes and native provenance. Complete means only
that those selected feeds were fetched. It does not mean the build ran a
scanner, the scan succeeded, coverage was complete or the repository is safe.

A collected report remains inert until a current analyst or administrator
opens the separate **Import SARIF** review and supplies actual scope, source
status, scan kind and completeness. The server derives asset, source, scan,
digest, collection time and optional SARIF invocation time from stored
evidence. Existing ingestion parses the queued report independently.

## Runtime configuration

The collection role accepts:

```text
ASPM_AZURE_DEVOPS_ENDPOINT=https://dev.azure.com
```

The endpoint is trusted network destination admission only. Organization,
project, repository, build, artifact, report path and PAT authority come from
the encrypted durable source/intent. The worker denies redirects, proxies,
HTTP, insecure TLS and a different network origin.

Helm exposes `collection.azureDevOpsEndpoint`; it defaults to
`https://dev.azure.com`. The existing Quadlet profile reads the equivalent
environment value from `/etc/aspm/collection.env`.

## Persistence and compatibility

Additive schema V12 adds nullable JSONB target/selection columns and expands
the existing named profile/record-kind checks. GitHub rows keep null additions,
their default list and DTO shape remain profile-specific, and their historical
idempotency binding bytes remain unchanged.

Azure DevOps stable asset identity includes organization, project and
repository. Repeated builds, connection IDs and upstream renames reuse that
asset without overwriting human ownership, environment, criticality or tags.

## Verification completed

- A1-A5: 37 owned fixtures passed together against real PostgreSQL, SeaweedFS,
  separate collection/raw roles and an owned normal-TLS native protocol.
- A5 built the exact pinned `69f3ef9` V11 production closure, migrated genuine
  API/native data through current V14, reopened it, finished an old queued
  GitHub collection and exercised the new Azure DevOps intake path.
- Twenty-five A4 subcases passed for active revocation, SQL-slot availability,
  binding tamper, leases/fences, exact role denial, evidence integrity,
  UTF-8/size checks, origin/query limits and ZIP safety.
- The real application UI browser case covers lazy profile setup, masked PAT,
  selected build/artifact/path, progress/records and separate SARIF review.
- All nine existing GitHub source browser cases pass unchanged.
- Real Helm client rendering passes defaults, selected endpoints, role
  isolation and unsafe/missing value rejection.

Ordinary Go tests/build and the web lock/type/build checks also pass. This is
developer and owned-fixture evidence, not a new independently signed capture.

## Remaining limits

- No authorized live Azure DevOps account was contacted. PAT permission,
  organization/project/repository visibility and vendor behavior remain
  unverified outside the synthetic protocol.
- Linux/Podman activation was not rerun for this increment.
- ADO-specific browser variants for lost acknowledgements, workspace switches
  and held late replies remain to be added. Shared GitHub source machinery
  retains its existing coverage for those behaviors.
- Other integration families remain deferred. The first M06 explicit
  two-finding merge/split and retention-preview increments are implemented.
  Physical archive/expiry execution is the next roadmap work.
