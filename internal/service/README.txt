Delivery runtime
================

cmd/delivery-worker runs service.Environment("delivery") and service.Run with
interrupt/SIGTERM cancellation. It owns one accepted app.DeliveryWorker and one
database pool. It does not open generic integrity jobs, S3 clients, bootstrap,
authentication or core/UI handlers. /healthz and /readyz describe the delivery
role and actual database readiness; storage is not required. Other routes,
including /api/v1/session and /, return 404.

Required environment:
  ASPM_DATABASE_URL and the existing explicit selected-schema DB settings
  ASPM_INTEGRATION_ENCRYPTION_KEY: canonical standard base64 for 32 bytes

Delivery-specific settings:
  ASPM_DB_MAX_CONNECTIONS: defaults to 1; values 1..100 are supported
  ASPM_DELIVERY_LEASE_DURATION: defaults to 15s; accepted range 250ms..1m
  ASPM_SLACK_ENDPOINT: defaults to https://slack.com; trusted HTTPS base only
  ASPM_DELIVERY_CA_FILE: optional regular PEM certificate file, at most 1 MiB

The ordinary service listen/TLS settings apply to health endpoints. Delivery
ignores unrelated S3/readiness/bootstrap environment values; it does not derive
the integration key or load an ambient credential chain.

Without a delivery CA file, outbound TLS uses normal system trust. An explicit
file replaces system roots with exactly the supplied valid PEM certificates.
Private keys, malformed/trailing non-PEM data and empty files are rejected.
Certificate and hostname verification remain enabled with TLS 1.2 or newer.
No machine trust store is changed.

The environment builds a bounded explicit HTTP transport with no proxy or
cookies, rejecting redirects and unrelated host/port dials before network I/O.
Run also validates caller-supplied client/transport/TLS/proxy policy before
database/schema/provider/listener activity. It clones the approved client,
transport and trust pool, applies the explicit CA policy to its private copy,
and preserves the configured endpoint, headers and native payload. Caller-owned
transport pools are not closed or mutated by Run.

The processing loop calls the accepted ProcessNext without interpreting or
rewriting outbox state. It waits 200ms only for false,nil idle results, with
immediate cancellation. Handled durable failures do not schedule retries or
backoff. Infrastructure errors stop the loop and service with a safe error.
Cancellation propagates to active native HTTP and bounded worker finalization;
uncertainty stays durable. There is no resend endpoint or exactly-once claim.

The owned Windows command-process fixture proves startup, TLS, durable receipt
and non-replay behavior. Its specific-PID cleanup is not evidence of Windows
OS-signal graceful shutdown; cancellation is separately exercised through Run.
The UI/core/queue/standalone-command path has also been exercised with an owned,
certificate-validated Slack-protocol fixture, including a confirmed receipt and
a dropped acknowledgement retained as uncertainty. No real Slack account was
used. Deployment/installer scheduling, live Slack authority, production key
rotation and operational rollout remain separate qualifications.

Collection runtime and core evidence reads
==========================================

cmd/collection-worker runs the collection service with standard context and
interrupt/SIGTERM cancellation. It owns one accepted CollectionWorker DB pool,
not an integrity queue, core handler or raw-intake storage capability. The
shared idle-only processing loop never requeues terminal native outcomes.

Core and collection use these six explicit process-local storage settings:
  ASPM_COLLECTION_S3_ENDPOINT
  ASPM_COLLECTION_S3_BUCKET
  ASPM_COLLECTION_S3_PREFIX
  ASPM_COLLECTION_S3_REGION
  ASPM_COLLECTION_S3_ACCESS_KEY
  ASPM_COLLECTION_S3_SECRET_KEY

For core, all six absent means no optional collection capability. If any is
present, all six must be valid and nonblank. No raw ASPM_S3 value is borrowed.
The actual core service forwards this separate read-only pointer into app.Open;
raw intake configuration and readiness behavior are unchanged.

Collection requires all six fields with its separate publisher identity, the
explicit DB configuration, and the same canonical 32-byte base64 integration
key format. MaxConnections defaults to 1. Unrelated raw storage, AWS/bootstrap,
assets and readiness environment settings are not inherited.

ASPM_COLLECTION_LEASE_DURATION defaults to 15s, bounded to 250ms..1m.
ASPM_GITHUB_ENDPOINT defaults to https://api.github.com and is trusted process
configuration only. ASPM_COLLECTION_CA_FILE uses the same reviewed bounded PEM,
normal hostname/TLS verification and explicit-root replacement policy as the
delivery role. The shared client builder rejects proxies/redirects/cookies,
restricts its dial origin and owns copies rather than mutating caller clients.
The stricter accepted source-client rules, including no ServerName override
and effective TLS range checks, remain authoritative before startup.

Default native limits are 32 requests, 8 pages, 50 records/page and 8 MiB.
Programmatic limits are forwarded to the accepted worker; no new environment
limit matrix, polling authority or scanner behavior is introduced.

All configuration is validated before EnsureSchema, resource opens or listener
binding. Source storage still performs no HeadBucket/ListBucket startup probe.
/healthz identifies collection; /readyz reports actual DB readiness and
storage:configured-not-probed. That label does not certify write permission or
provider access. /api/session, /api/v1/session and / return 404.

Owned runtime tests prove distinct real storage keys, core-RO GET and PUT
denial, publisher PUT, actual core-service evidence reads, context cancellation
and a separately built command processing a new intent without resurrecting an
old failed job. Test taps add only a narrower owned-prefix guard, not synthetic
storage authorization. Specific-PID command cleanup is not OS-signal graceful
shutdown evidence.

The Sources UI/core/standalone-worker path was also exercised through real HTTPS
with an owned GitHub-protocol gateway and distinct core-reader/publisher keys.
Complete and partial collections retained exact evidence downloads and human
asset edits without normalizing findings. A separate real metadata-refresh check
confirmed the displayed target and queued binding agree with received source
revisions. Neither check used a live GitHub account. Image and explicit opt-in
Helm/manual Quadlet artifacts are available; their separate owned-cluster and
native key-permission checks are recorded in docs/m02-runtime.md. Installer
provisioning/scheduling, native systemd activation, live authority, bearer refresh
and operational rollout remain separate work.

Assessment runtime and core scope
=================================

cmd/assessment-worker uses service.Environment("assessment") and service.Run
with the ordinary interrupt/SIGTERM context. It opens the accepted independent
app.AssessmentWorker with one DB pool, not a core HTTP application, integrity
queue, auth/static handler or storage client. MaxConnections defaults to 1.
The existing database/schema/listen/TLS settings and generated bounded worker
identity apply. There is no assessment worker-ID environment override.

Core reads only ASPM_ASSESSMENT_SCOPE from the new assessment settings. Absent
or empty leaves assessments unavailable without disabling ordinary asset,
import or AI configuration APIs. A nonempty value is forwarded to app.Config,
after pure validation before schema/DB/listener activity. It must be nonblank,
valid UTF-8, at most 128 bytes, with no surrounding whitespace or control
characters. Configuring a scope, profile or grant never starts a core worker.
Other roles ignore all new assessment settings; core ignores worker-only limits
and CA settings. Existing core encryption/storage parsing is unchanged.

Assessment requires explicit nonblank ASPM_ASSESSMENT_SCOPE. Its durable scope
and limits must agree with the accepted backend configuration for that schema.
ASPM_INTEGRATION_ENCRYPTION_KEY may be absent for local keyless profiles. If
supplied, including an empty value, it must be canonical standard base64 for
exactly 32 bytes. A hosted profile without its worker key cannot dispatch.
No ambient provider/AWS credentials, raw S3 settings, bootstrap/assets,
readiness preparation, collection storage or static gateway are inherited.

The following ASPM_ASSESSMENT_ settings default only when ABSENT:
  LEASE_DURATION           15s     range 250ms..1m
  AUTHORIZATION_INTERVAL   100ms   range 10ms..1s, strictly shorter than lease
  REQUEST_TIMEOUT          10s     positive, at most 30s
  REQUEST_WINDOW           1m      range 1s..1h
  MAX_CONCURRENT           1       range 1..16
  REQUESTS_PER_WINDOW      30      range 1..1000
  MAX_INPUT_BYTES          32768   range 1..32768
  MAX_OUTPUT_TOKENS        1024    range 1..32768
  MAX_RESPONSE_BYTES       65536   range 1..131072
Supplied empty, invalid, overflowing or out-of-bound values fail, not clamp or
fall back. Programmatic Config fields are forwarded directly to the existing
pure app.ValidateAssessmentWorkerConfig before schema or external I/O. These
are durable request/concurrency limits, not account/model/token-rate/cost caps.

ASPM_ASSESSMENT_CA_FILE absent/empty uses normal system trust. An explicit
regular certificate-only PEM file, nonempty and at most 1 MiB, replaces those
roots. The shared strict PEM reader rejects junk, private keys and invalid
certificates. TLS/hostname verification stays enabled with TLS 1.2 or newer and
a supported effective range. No trust-store or OS policy is changed.

The explicit bounded HTTP client has no proxy/cookies/redirect following,
ServerName override, DialTLS bypass or default-client fallback. Run owns copies
of the approved client, transport, roots and key, closes its resources and does
not mutate/close caller-owned transports. The current persisted profile/grant
selects the endpoint/model/key at dispatch; the accepted worker binds that
target. ASPM_MODEL_ENDPOINT or caller JSON headers grant no authority.

For environment-created assessment clients, ASPM_ASSESSMENT_REQUEST_TIMEOUT
bounds both the total request and the response-header wait. Programmatic client
header policy, the shared 5s connect/TLS handshake bounds and other roles'
defaults remain unchanged.

The unchanged shared loop waits context-aware for 200ms only after false,nil,
continues immediately after true,nil and stops visibly on infrastructure error.
No service retry/backoff, new attempt, resubmission or quota allocation is added.
Cancellation reaches active native HTTP and owned resources. Dispatched
uncertainty, current-authority checks and admission history remain durable
across independent command opens; a restart cannot resend a marked attempt.

/healthz returns apiVersion:aspm/v1alpha1, service:assessment, status:alive.
/readyz runs actual worker Ping and reports service:assessment, status:ready,
database:reachable, storage:not-required, admission:configured, the explicit
scope, provider:not-probed and pipeline:inspect-job-state-separately.
DB failure returns 503, never a cached ready result. Readiness/startup do not
probe a model, validate a provider key or promise available quota.
/api/session, /api/v1/session and / return 404.

The runtime contract exercises actual core Environment/Run, reviewed API jobs,
an independent assessment service and the compiled command against owned
synthetic native HTTP/TLS fixtures, including Run cancellation and crash/reopen
uncertainty with new explicit work. Specific-PID command cleanup is NOT proof
of graceful OS-signal handling. No live provider/model/account, model quality,
retention or runtime processing geography is qualified.

Source and host image inventories now include this command, retaining the core
default. Helm deployment is explicitly selected with assessment.enabled=true
and a valid assessment.scope; a nonempty scope also preconfigures core while
the worker is disabled. Worker limits remain assessment-only. A complete
integrationKeySecret is optional for assessment, allowing keyless local
profiles without changing delivery/collection key requirements.
This first chart profile uses normal system trust and supplies no CA path,
Secret mount or model/endpoint knobs. Private CA files and the runtime
ASPM_ASSESSMENT_CA_FILE setting require explicit manual operator provisioning.
The manual aspm-assessment.container uses only /etc/aspm/assessment.env.
The Linux installer does not copy/start this unit or provision that protected
file. See docs/m02-runtime.md for the artifact profile and its limits.
Separate September 21 qualification built both seven-command image paths and
started the actual source-image assessment role in the owned Kubernetes lab.
The executable hash, explicit shared scope, database readiness, role environment
isolation and health-only surface were checked. No provider job was submitted
there, and protected host files/native systemd activation remain operator work.
Source/render/native-generator checks alone do not activate a worker. This is
not whole-M11 completion, production network isolation or release acceptance.
