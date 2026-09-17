# aspm

Self-hosted application security posture management, working name **aspm**.
Module: `github.com/bahadrdsr/aspm`. Original project and SDK code: Apache-2.0.

## Current state: engineering preview

M00 was independently accepted at commit
`9692bcf526bc6a2cd31818c379cf124f17910cf0`. The repository now includes reviewed
runtime, installer and application UI increments beyond those foundations.
The full milestone roadmap is not complete, and this is not a supported release.

The application now has a real PostgreSQL/S3 backend: authenticated workspaces,
roles, assets, queued report intake, findings and scan history, CSV exports,
and independently processed report snapshots. The React interface supports
login, workspace selection, asset creation/editing, local report upload with
server-driven import status, finding evidence, observations and analyst notes,
live posture reports with saved snapshots, and Slack connections with explicit
finding-notification previews and delivery history, plus selected GitHub source
configuration, explicit collection intents and raw record/evidence views. Other integration setup remains
unfinished. Reports and finding triage have independent source acceptance and
owned HTTPS workflow qualification.

Core API, ingestion and reporting run as separate processes. Core and ingestion
use distinct scoped storage identities; reporting is database-only. Real local
flows have been exercised, but further backend authorization/concurrency,
deployment, recovery and capacity gates remain open. PostgreSQL role separation
and production network isolation are not yet qualified.

Eight native integration families and four configurable AI provider adapters
exist as libraries. Slack notifications and selected GitHub repository collection
have durable application workflows; orchestration for the other native families,
persistent AI assessment jobs and live vendor/model qualification remain incomplete.
Verification supports
approved deterministic synthetic evidence only, not exploit execution or
autonomous offensive tools.

Linux/Podman Quadlet and Kubernetes/Helm are the production targets. The installer
now supports the configuration wizard, diagnostics, signed target-bound planning,
managed apply, resume/reapply, status and data-preserving uninstall for bounded
profiles. A real owned Kubernetes lifecycle has been exercised; native Linux
activation, backup/restore, general upgrades and release qualification remain
unfinished. See `docs\m02-runtime.md` and `docs\m03-configuration.md` for the
implemented behavior and deployment boundaries.

## Run the static UI preview

Use Go 1.27.1, Node 22.20.0 and npm 10.9.3. From the project root on Windows:

```powershell
Set-Location .\web
$env:NODE_USE_SYSTEM_CA = '1'
npm.cmd ci --ignore-scripts
npm.cmd run build
Set-Location ..
go run .\cmd\aspm-dev --assets .\web\dist --listen 127.0.0.1:8080
```

Open `http://127.0.0.1:8080`. This development host serves static assets only:
its data APIs remain unavailable, so it cannot demonstrate authenticated
application workflows. The component gallery is explicitly synthetic and never
an API-error fallback. The host refuses public bind addresses because it has no
authentication. Do not expose it through a public reverse proxy.

For hot reload, run `npm.cmd run dev` from `web` while the Go development host
runs on port 8080. Vite proxies only `/api` to that loopback host. Browser
acceptance tests instead mock the declared HTTP boundary, including the bounded
asset/import and snapshot-creation writes, not React components. Their synthetic corpus is never in
production imports; these checks do not prove live backend permissions.

For the actual application, use `cmd\core-api`, `cmd\ingestion` and
`cmd\report-worker` with an existing database, existing evidence bucket and
protected role-specific configuration described in `docs\m02-runtime.md`.
Do not share an operator S3 credential across those processes.

### Application UI workflows

In **Assets**, create an asset or edit a returned inventory row. The owner
selector supports unassigned, yourself, or preserving its existing owner;
the service verifies membership and write authority. Viewer controls are
read only. **Load more assets** in inventory or the import form explicitly follows
the service's native cursor. The default first read remains the bare assets GET;
no server search, offset or automatic page drain is introduced. Both views share
the accumulated, ID-deduplicated rows. A selected import asset stays selected as
more pages arrive, including while editing report format or scope.

Continuation failures retain confirmed rows, and **Retry assets** repeats the
failed cursor. **Refresh assets** and post-save refreshes update the first page
without discarding other loaded rows; those pages can be older. Canonical write
acknowledgements update the visible cache immediately, before post-save reads
finish, and take precedence over reads started before them. Later reads can
receive newer facts. Loaded counts and the last received workspace total are
not an atomic full-directory snapshot. Rows are kept in memory only for the current
Assets view, not browser storage; leaving it or changing workspace/session clears
the pages and cancels old reads. This is manual paging, not an unbounded-memory or
virtualized inventory performance claim.

**Import report** offers seven existing backend profiles: SARIF 2.1.0, Trivy JSON
(SchemaVersion 2), ZAP JSON (@version 2.16.1), Gitleaks JSON (a v8-style array with
Fingerprint), Generic JSON, Generic CSV and Manual JSON. XML, YAML, arbitrary
scanner formats and AI prose extraction are not supported. Filename and MIME
type never override the explicitly selected format.

Generic JSON arrays and CSV header/quoted rows use the same displayed eight-field
literal mapping. SARIF, native and manual profiles omit mapping entirely. Manual
input is a human-authored JSON object requiring nonblank `sourceFindingId`, `title`
and a `sourceLocation` object, which may be empty. Severity, description, impact,
remediation and location URI/line are optional. Source IDs/titles are bounded to
4096 UTF-8 bytes, location URI to 8192 bytes, and a supplied line to a nonnegative
signed 32-bit integer. Plain prose is never silently converted to findings.

The browser preserves exact UTF-8 report text, including CRLF, trailing newlines,
CSV quoting and multibyte characters, and sends one JSON request with
the selected asset and explicit source/scan/scope metadata. The encoded request
and raw file are each bounded to 8 MiB; smaller operator limits still apply, and
larger limits are not discovered automatically. Invalid UTF-8 and oversized
requests fail without repair or truncation. Unknown source scan time stays null;
collection/import times and a Gitleaks commit Date never replace it.
No scan tools, mapping scripts or source-URL fetches run. Selecting a format/file
does not upload anything, and a rejected format is never retried as another parser. Acknowledgement
is not processing success: **Refresh import status** displays the service's
queued, processing, succeeded or failed response and failure diagnostic.
This page keeps only its latest receipt in memory, without automatic polling
or a browsable import history. Closing a pending form cannot undo a request
already accepted by the service.

Finding detail retains each returned observation, literal unmapped source
context, analyst notes and disposition. Source scan, comparable source freshness,
collection and import times remain distinct. Source-inferred resolution and
accepted risk do not alter human workflow or claim independent verification.
Admins and analysts can **Assign to me** with one activation, explicitly
**Unassign**, or select a workflow and **Save workflow** in the same dialog.
Confirmed facts and matching loaded Work rows use the full PATCH response,
including the service's owner display name. Earlier Work reads cannot undo that
acknowledgement; a subsequent explicit refresh can receive newer server facts.
An owner change that removes a row from the current filter is explained, with
a safe Work focus fallback rather than an invented matching row.

Risk acceptance is a deliberate disposition and optional RFC3339 expiry.
Blank means no expiry. **Save risk acceptance** sends only the chosen risk
fields; expiry-only edits do not resend owner, workflow or disposition.
Failures retain the draft and the service's previous expiry and expired flag.
Human resolution is not independent verification.

**Add note** accepts literal multiline text up to 8,192 UTF-8 bytes, without
trimming or truncation. The encoded note request must also fit the service's
32 KiB JSON limit. Only a validated HTTP 201 receipt adds a note; confirmed note
IDs are reconciled with canonical responses without merging different notes
that happen to contain identical text. Drafts stay in the open dialog only.
Viewer sessions have no write controls, and the server still authorizes every
action. Closing cancels pending browser actions but cannot roll back a request
already committed by the service. Finding-action source review and real HTTPS
qualification remain separate from the synthetic browser checks.

In the finding dialog, **Load more observations** and **Load more notes** follow
their separate native cursors through the existing finding GET. The default
initial read stays bare, and only the chosen stream advances; the other may
repeat its previously requested page. Both returned pages merge by server ID,
preserving loaded tails and distinct notes with identical text. Counts describe
loaded records, not an invented total or an atomic full-history snapshot.

Pending or unavailable continuation reads retain source facts and both histories;
**Retry** repeats the exact failed query. Permission/missing-detail denials withhold
cached source/history until an authorized read succeeds. Validated note and workflow
ACKs merge immediately, without a follow-up GET as a success proxy. Capped mutation
responses do not replace loaded history or rewind either continuation. Local
request/ACK ordering prevents older reads from rolling back confirmed facts;
a genuinely later requested read can supply newer canonical metadata.

These are manual reads, not automatic polling, source URL fetching or proof.
The native dialog, note drafts and Work context remain in place during paging.
Closing, workspace changes, logout and session rejection clear the scoped history
and cancel old requests. No history, note, report or authentication data is stored
in browser storage.

In **Integrations**, **Sources** manages one selected GitHub `owner/repo` per
`github-cloud-app` source, separately from the catalog and Slack destinations.
Admins supply a name, masked existing installation bearer and explicit enabled
state. Edits send only changed fields; a blank replacement bearer is omitted.
Credential presence and returned revisions are metadata, not connected or
live-verified status. Missing encryption configuration is an operator issue;
the browser never requests an operator key, endpoint, headers or OAuth flow.

Admins and analysts can open **Collections**, review the selected repository and
explicitly confirm **Collect source**. Only an idempotency key is submitted.
A 202 means queued, not completed. Lost acknowledgements retain the same intent
in scoped memory for user-confirmed replay, including across dialog closes; no
automatic retry or fresh-key duplicate is attempted. Collection evidence storage
must be configured by the operator. Current server permissions remain authoritative.

An authorized Sources refresh updates the selected source summary and any open
collection confirmation with the newest received revision for that source ID.
Later pages that omit the selected ID retain its last authorized metadata,
including a disabled state. Older revisions do not replace newer selected facts.
These updates preserve read-only collection history and its immutable original
binding; they neither enqueue work nor resolve a lost acknowledgement. Explicit
replay still uses the same unresolved key and can return a binding conflict.
This reflects received metadata, not an atomic browser/server snapshot guarantee.

Collection status, history and records are manual reads with native cursor pages.
`complete` means selected feeds were exhausted, not scanner success, normalized
findings, closure or independent verification. Partial results retain their raw
records, gaps and native failure data; Retry-After is display data only. Record
`rawURL` values are provenance and are never fetched. Native update time is not
source scan time, and missing scan/run identifiers stay unknown.

**View evidence** reads the stored octet-stream, checks its byte size and digest,
and displays UTF-8 literally or provides an exact-byte download when text decoding
is unavailable. No trimming, JSON reserialization or active markup is performed.
Denied evidence stays withheld through later unavailable retries until an
authorized read succeeds. Workspace/session changes clear forms, collections and
evidence and abort old requests. The complete UI/core/collection-worker/PG/S3 path
has been exercised through real HTTPS and an owned certificate-validated
GitHub-protocol fixture. Complete and partial collections retained exact evidence
bytes and human asset edits without creating normalized findings. This is not
live GitHub installation or repository-access certification.

To run this initial source profile, configure core and the collection worker
with the same protected `ASPM_INTEGRATION_ENCRYPTION_KEY`, encoded as canonical
standard base64 for an independently generated 32-byte key. Supply the selected
database URL/schema and all six `ASPM_COLLECTION_S3_*` settings documented in
`internal\service\README.txt`. Core needs a read-only collection-evidence identity;
the worker needs its own publisher identity for the same selected bucket/prefix.
Never substitute the raw-intake or operator storage credentials.

After provisioning the worker's protected process environment, run:

```powershell
go run .\cmd\collection-worker
```

The default source endpoint is `https://api.github.com`; only trusted process
configuration can choose an approved HTTPS gateway and CA file. Readiness reports
database availability and `storage:configured-not-probed`, not provider authority
or verified S3 permissions. Collection image/Helm/Quadlet packaging, installer
provisioning and installation-bearer refresh remain separate unfinished work.
Opening the Sources UI does not start a worker or schedule collection.

In **Integrations**, **Connections** lists workspace Slack destinations separately
from the eight catalog families. Admins can create and edit a name, C/G channel ID,
masked opaque bot token and explicit enabled state. A blank edit token preserves
the stored credential; other edits send only changed fields. Credential presence
and revision are metadata, not a connected or live-verified claim. Server permission
denials remain authoritative. If credential encryption is unavailable, an operator
must configure the service; the browser never requests an encryption key.

In the existing finding dialog, admins and analysts can **Notify**, review the
selected enabled connection and a title/severity/asset/link-only preview, then
explicitly confirm. Opening or cancelling a preview does not enqueue anything.
Original evidence, source code, notes and remediation text are excluded. A 202
means queued, not sent. After acknowledgement, **Selected delivery** uses the exact
immutable server payload, link, times and receipt rather than reconstructing them.
**Delivery history** and Connections follow native limit/cursor pages; disabled
connection metadata remains readable.

Delivery/history refreshes are manual reads, never sends or polling. Failure codes,
native codes, HTTP status and Retry-After remain literal outcome data. Uncertain
outcomes have no resend action. A lost enqueue acknowledgement retains the same
intent key in scoped memory, including across dialog closes, for explicit replay;
closing is not server rollback. Workspace/session loss clears drafts, intents and
protected views and aborts old requests. No connection or notification data is
persisted in browser storage. These UI workflows neither change findings nor
independently verify a Slack account. The complete UI, encrypted connection store,
durable queue and separate delivery command have been exercised together through
real HTTPS and an owned certificate-validated Slack-protocol fixture. Confirmed
and lost-acknowledgement outcomes remain distinct, with no automatic resend.
This is not live Slack installation or channel-authority certification.

For this initial managed notification profile, core and `cmd\delivery-worker`
must receive the same independently generated 32-byte key through protected
`ASPM_INTEGRATION_ENCRYPTION_KEY` configuration, encoded as canonical standard
base64. Do not derive it from database, bootstrap or storage credentials.
The delivery process also needs the selected database URL/schema; it does not
need S3 or bootstrap credentials. After provisioning those values in its own
protected process environment, run:

```powershell
go run .\cmd\delivery-worker
```

The default outbound base is `https://slack.com`. Only trusted process
configuration may select an approved HTTPS gateway and its optional CA file.
See `internal\service\README.txt` for transport, lease and shutdown behavior.
Helm and manual Quadlet deployment of this role are explicit opt-ins; see
`docs\m02-runtime.md`. Installer key/env-file provisioning and automatic scheduling,
plus production key rotation, remain unfinished. The presence of the UI does not
start a worker automatically.

In **Reports**, **Live overview** displays the service's exact totals, all-finding
severity counts, coverage, as-of time and freshness bounds. Edit **Freshness days**
(1 through 365, initially 7), then explicitly **Refresh report** to apply the
window. Unknown source freshness is not an unscanned count or proof of a current
scan. Accepted risk and source-inferred resolution are not independent verification;
the service's verification limitation remains visible.

Admins and analysts can **Create snapshot** with a name and freshness window.
A 202 response means queued, not completed. **Refresh snapshot** reads actual
worker state, retry diagnostics, failure or completion. Completed metrics retain
their saved as-of time when the live overview changes. Viewers can read history
and details but cannot create snapshots; server authorization remains authoritative.
**Load more snapshots** follows the returned cursor, retains loaded rows on a
continuation failure and retries that same cursor. Creation refreshes history;
**Refresh history** otherwise starts again from the first page. Workspace or
session changes clear reports and cancel old reads. Denied or missing selected
details remain cleared until an authorized response succeeds. There is no
automatic polling, report storage in the browser, trend engine or Reports export UI.
Synthetic browser workflows do not qualify the real reporting worker, durable
storage or live authorization; those require separate backend and HTTPS checks.

The lockfile uses canonical registry archive URLs and exact versions. In the
current maintainer environment, the configured mirror's nested archive URLs
need `--replace-registry-host=never` when updating dependencies, followed by
`node scripts\normalize-lock.mjs` from `web`. This preserves versions and
integrities. Never disable TLS or switch to a default preview tag.

### Read the contracts

`docs\contracts\v1alpha1` contains installation/provider schemas, safe resolved
examples, the installer contract, integration/provider catalogs, UX budgets,
capacity profiles and synthetic lifecycle cases. Schema IDs use `.invalid`
identifiers and tests forbid external schema loads.

- `contracts\CONTRACTS.txt`: executable contract and test-author handoff.
- `docs\adr\0001-m00-decisions.md`: identity, contribution, source/CI/registry and stack decisions.
- `docs\architecture.md`: service ownership, ingestion boundaries and lifecycle safety.
- `docs\dependencies.md`: license findings and distribution-review boundaries.
- `docs\design\m00-journeys.html`: original static storyboard, not an implemented UI.
- `docs\operator-research.md`: operator assumptions and bounded comparison decisions.
- `CONTRIBUTING.md`: contribution and independent-review rules.

M01 adopts three independently approved historical shadcn/Animate UI source
snapshots, with exact provenance and retained MIT notices under
`web\src\components\provenance.json` and `web\public\notices`.
Current Animate UI main and its registry remain unapproved. See
`web\tests\reviews\m01-source-approval.md` and `docs\m01-foundation.md`
for source-usage conditions, dependency review and current limitations.

## Validate

The module requests Go **1.27.1** via `toolchain` and has a Go 1.27.0 language
minimum. From the repository root on Windows:

```powershell
.\contracts\Test-M00.ps1
```

That M00 runner keeps Go caches and scratch work under `.cache` and restores its
environment. M00 tests remain network-independent after restore. UI foundation
checks add browser and static-build gates:

```powershell
Set-Location .\web
npm.cmd run test:install-browser
npm.cmd run typecheck
npm.cmd test
npm.cmd run build
npm.cmd run test:capture
Set-Location ..
go test -json -count=1 -mod=readonly .\contracts
go test -count=1 -mod=readonly .\internal\devhost
go build -mod=readonly .\...
node scripts\check-config.mjs
node scripts\sign.mjs self-test
```

`node scripts\check.mjs` combines source/browser/build/configuration checks and
requires an explicitly provisioned, independently trusted oracle checker, key
and pinned configuration. See `scripts\ORACLES.md`; there is no unsigned or
automatic-baseline fallback. Actual API/worker and storage-permission suites
under `tests` require the explicitly supplied owned fixtures documented in their
contracts. Passing the UI checks alone does not certify those boundaries.

`node scripts\build.mjs` recreates the dedicated `.artifacts\development`
output with the host, UI, notices, SBOM and digest manifest. It records the
source revision and dirty-worktree state and never publishes anything.

Review screenshots are generated in `web\.artifacts\review`, not treated as
automatically approved visual baselines. Source review, controlled protocol
fixtures, real local service checks, native deployment and release acceptance
are separate claims. A local Kubernetes lab exists; protected CI, native
Linux/systemd qualification and a supported release remain unfinished.
