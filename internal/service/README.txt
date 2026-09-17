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
revisions. Neither check used a live GitHub account. Deployment packaging and
installer scheduling, live authority, bearer provisioning/refresh and operational
rollout remain separate work.
