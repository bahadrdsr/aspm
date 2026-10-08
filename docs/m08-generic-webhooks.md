# M08 generic outbound webhooks

Implemented on October 8, 2026.

V22 adds a non-native `generic-webhook-v1` delivery profile inside the existing
connection, preview, outbox, worker, history, and notification-policy framework.
It is not a ninth native integration family and does not add arbitrary scripts,
templates, methods, headers, or provider status linkage.

## Operator and workspace authority

Operators configure up to 16 exact canonical HTTPS origins through
`ASPM_WEBHOOK_ORIGINS`. Core and delivery receive the same list. Workspace
administrators can select an exact approved origin and explicit path, but cannot
add a destination, change transport policy, use HTTP, add query credentials,
follow redirects, configure a proxy, or override method, headers, timeout, or
TLS behavior.

The application stores only the origin, path, and `hmac-sha256` metadata. The
HMAC secret is 32 through 4,096 bytes, encrypted with the existing independent
integration key and profile/workspace/connection authenticated data. It is
write-only and never appears in responses, logs, URLs, browser storage, or
provider headers.

## Explicit review and queueing

Admins and analysts can open a local finding preview. It contains only:

- Finding title.
- Severity and asset name.
- Trusted finding link.
- Selected origin, path, and signature method.

Queueing requires explicit confirmation and the current preview digest. A 202
means durable queued work, not a receiver request. Same-intent replay returns the
original delivery. A new explicit idempotency key creates a separate notification
intent; there is no automatic resend.

## Fixed signed request

The delivery worker sends exactly one HTTPS POST with JSON content and these
server-owned headers:

- `X-ASPM-Event: finding.notification.v1`
- `X-ASPM-Delivery-ID`
- `X-ASPM-Signature: sha256=<lowercase HMAC-SHA256>`

The signature covers the exact body bytes. The body contains the API/event and
delivery/workspace/finding identities, manual or policy trigger identity, and
the bounded notification. Evidence, notes, remediation, unmapped scanner fields,
credentials, approval references, source text, and executable content are not
included.

HTTP 2xx means endpoint accepted, with no downstream-processing or receipt
claim. HTTP 429 is rate-limited. Other 4xx responses fail. Timeouts, dropped
acknowledgements, malformed transport results, cancellation after a possible
POST, and HTTP 5xx responses are uncertain. Redirects are blocked and terminal
outcomes never retry automatically.

## Notification policies

V22 allows an approved notification policy to select a generic webhook
connection. Policy evaluation still performs no provider I/O and uses the same
epoch, connection revision, unique policy effect, stale-origin, and
invalid-payload behavior. Generic webhooks never use Jira ticket effect keys.

## Limits

This increment does not provide arbitrary body templates, custom headers,
Node-RED-style integration flows, bidirectional callbacks, provider status
linkage, live receiver certification, automatic finding resolution, or delivery
retry/reconciliation.
