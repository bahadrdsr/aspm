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
