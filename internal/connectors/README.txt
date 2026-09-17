M08/M09 first native adapters
============================
Entry points: connectors.OpenDelivery(ctx, DeliveryConfig) and
connectors.OpenCollector(ctx, CollectorConfig). Both validate without I/O.
Public DTOs and errors are in types.go. No new module dependencies.

Delivery profiles
-----------------
jira-cloud-v3: authorized bearer, project KEY and numeric issue type; bounded
create-field metadata preview, custom string fields, ADF v1 create, status-only
read. StatusMap accepts open/in-progress/pending-retest, never closure.
Send does not implicitly run Preview or create metadata requests.
teams-workflows-channel: the complete, reviewed standard-channel Workflow URL.
Adaptive Card 1.4; 202 is accepted, not proof that a channel received the card.
The URL is the credential; this profile never adds the configured bearer token.
Its query must contain exactly one nonempty, whitespace-free sig credential;
missing or malformed signed-URL credentials fail construction before preview/I/O.
slack-workspace-bot: supplied bot token and selected C/G channel ID, native
chat.postMessage with accessible text, plain-text section and link button.
Unfurling is disabled. HTTP 200 ok=false retains native error semantics.

Durable outbox/application boundary
-----------------------------------
The caller MUST authorize the action, persist an immutable intent binding the
workspace/finding/payload/profile/destination, serialize delivery with an
outbox lease, and supply its trusted latest Action.Prior snapshot. ApprovalRef
is a required reference, NOT an authorization engine or proof of approval.
The adapter compares prior workspace/intent identity. Confirmed or accepted
replay returns the prior receipt without I/O, including after opening a fresh
adapter. Uncertain replay returns ErrUncertain without I/O.
Persist receipts AND uncertainty before retry scheduling. There is no in-memory
deduplication, durable storage, distributed lease, or exactly-once guarantee.
A crash between vendor acceptance and receipt persistence needs reconciliation.
Do not replay an in-flight/unknown intent as a new action with no Prior.
No writes are retried automatically. Lost/malformed acknowledgements and 5xx
are uncertain; redirects/unapproved actions blocked; auth/field errors failed;
429 rate-limited. RetryAfter is data, never a sleep. Jira remains open until a
separate verification workflow, regardless of its external issue status.

Collection profiles
-------------------
github-cloud-app: selected owner/repo, installation bearer, REST 2026-03-10.
Repository and bounded code-scanning alerts; same-repository Link validation.
GitHub 403 quota indicators remain distinct from ordinary permission denial.
Retry-After and primary X-RateLimit-Reset are metadata only, capped at 24 hours;
missing/invalid delays remain unknown (zero), without sleeps or retries.
Alert analysis_key/ref/tool remain native context in Raw, not a made-up run ID.
gitlab-com-artifacts-v4: supplied PRIVATE-TOKEN/read_api; numeric or namespaced
project plus numeric pipeline/job IDs and one relative artifact file. Verifies
project/pipeline/job linkage; preserves repository, pipeline, job, report bytes.
ado-services-build-artifacts: organization name, project/repository UUIDs,
numeric build ID and artifact name/path. Basic (:PAT), API 7.1. Validates the
returned same-origin/org/project/build/artifact download URL and exact query.
Reads only the exact regular file in a bounded ZIP, never extracts to disk.
aws-commercial-ec2-securityhub: supplied CredentialsProvider retrieved only at
Collect, selected 12-digit account and commercial region. EC2 DescribeInstances
Query 2016-11-15 owner-id filter plus regional/account-scoped Security Hub
GetFindings ASFF 2018-10-08. Existing official AWS SigV4 signer, no SDK retryer,
credential chain, STS, metadata discovery, or cloud mutations.
azure-public-resourcegraph-assessments: one subscription UUID, supplied ARM
bearer. Fixed projected ResourceGraph query, 2022-10-01, plus same-subscription
Defender Assessments List 2020-01-01. Scoped/versioned nextLink only.
Assessment parents may be the selected subscription, one of its resource groups,
or its resources; IDs must retain the exact safe parent/assessment relation.

API bases are explicitly configured HTTPS origins or API gateway prefixes,
without query/fragment/userinfo; native routes are appended to that base.
Teams is the full URL exception. Credentials and authorization of configured
endpoints are the caller's responsibility, not discovered or provisioned here.

Finding DeepLink values are HTTPS browser links, not provider request targets.
They may retain a client-side hash route such as /#/work?finding=<id>; the exact
link reaches the native payload unchanged. HTTP endpoints still reject fragments.
Browser links reject credentials, invalid ports, oversized values and raw or
decoded control characters, including those in query/fragment text.

Bounds and evidence
-------------------
Limits defaults: Requests=32 per operation, Pages=8 per feed, PageSize=50,
Bytes=8 MiB per response/request/extracted report. Maxima: 128/64/100/32 MiB.
EC2 requires PageSize >= 5. ZIP directory entries are capped at 4096.
Assessments List has no page-size request parameter; response bytes and page/
request limits bound that feed. No unsupported query parameter is invented.
Contexts and injected client transport/timeouts are retained (30 seconds if
the client has no timeout); client objects are not mutated. Client redirects
are disabled and cookie jars are not used. A supplied transport must itself
honor context and must not add retries, redirects, credentials or URL rewrites.
There is no adapter retry loop, including for read-only cloud POSTs.

Each result keeps caller workspace/source/run/scope identity and collection
time. Partial failures retain valid prior records, Complete=false, nonempty
Gaps, typed errors, and RetryAfter. Continuation is an unfinished-feed
diagnostic only, not a certified cross-call resumable checkpoint.
Complete means selected collection feeds were exhausted, not scanner success,
safe absence, complete tenant inventory, or finding closure. Raw source JSON,
EC2 instance XML elements and report bytes are not reserialized. Full ARM IDs,
ASFF product/finding IDs, native pipeline/build IDs and source update times
remain separate. Only a single-run SARIF 2.1.0 report with one invocation's
declared startTime supplies SourceScanAt; report parsing stays outside here.
Error exposes Code/NativeCode/HTTPStatus/RetryAfter without provider messages,
credential-bearing URLs or raw response bodies. Use errors.Is for ErrAuth,
ErrRateLimited, ErrUncertain, ErrRequiredFields, ErrScope, ErrLimit,
ErrUnavailable, ErrProtocol, ErrUnsupported and context cancellation.

Frozen limitations and verification
-----------------------------------
Not implemented: Jira edits/transitions, tenant/OAuth/Entra/STS bootstrap,
channel ownership or tenant installation, CODEOWNERS/team inventory, pipeline
triggering, GitLab CDN redirect downloads, arbitrary artifact URLs or directory
extraction, all cloud resources/regions, network scanning/code execution,
vulnerability reproduction, durable outbox/jobs/UI, M10 exports, deployed
workers, or live vendor certification. Parent application/storage/job/UI work
must wire these APIs separately. Backend catalog and account setup are untouched.

Run from the module root:
  go test -mod=readonly -count=1 -timeout=45s .\tests\connectors .\internal\connectors
The nine independent M08/M09 gates use synthetic TLS HTTP fixtures and a
forwarding-only production_bindings_test.go. They demonstrate native protocol
mapping, NOT authorized real-account operation. Every live profile remains
UNVERIFIED pending separately authorized, timestamped vendor evidence.
Frozen route/version provenance: tests\connectors\CONTRACT.txt.
