M11 provider adapters
====================
Open constructs a no-I/O, immutable profile/policy snapshot. Assess enforces
workspace/task/data-class approval and the configured provider base before
making one bounded native request. Credentials stay in headers, never prompts.
Profiles and request revisions remain separate from the finding domain.

Initial profiles:
- openai: Responses /v1/responses, store=false, native structured text format.
- azure-foundry: OpenAI v1 Responses with explicit deployment, api-key and
  separate configured/returned model identities.
- anthropic: native Messages, stable version header and output_config schema.
- local: explicitly reviewed OpenAI-compatible Chat Completions capability.

No tools are registered or executed. Returned tool calls are refused. An
incomplete, invalid, oversized or ungrounded response never returns an
assessment-shaped success. Missing usage is unknown rather than fabricated zero.
Anthropic cache accounting differs from OpenAI/chat totals and is handled
separately. Retry-After is returned as scheduling information, not a sleep.

Rejected structured output exposes OutputError with bounded Kind values:
schema, refusal, grounding or incomplete. errors.Is(err, ErrOutput) remains
true for existing callers; errors.As exposes the distinction without model
text in the error. Native parsing happens here once, not again in the worker.
Available request/model/stop/usage/retry metadata remains on Result when an
assessment is rejected. Auth, rate, provider, capability, response-limit and
tool-output errors retain their existing independent sentinel meanings.

The adapter's cloned HTTP client does not follow redirects or forward ambient
cookie jars. Direct users of Open must supply their trusted transport policy;
the durable worker's stricter constructor checks are a separate boundary.
No automatic retry or fallback is implemented; MaxAttempts is a ceiling, not
a request to repeat.
Local-only rejects hosted profile families even if they point to loopback.
The configured inference runtime still needs its own enforced egress policy:
an adapter cannot prove that a local proxy is not forwarding requests.

Tests in tests/analysis were independently authored before this implementation.
They use synthetic TLS HTTP endpoints and validate native mappings, policy,
errors, usage and grounding. They DO NOT verify a vendor account, model quality,
immutable model identity, provider retention configuration or distributed quotas.
No hosted service or local model is required merely to build/test this package.

Use a trusted, authenticated configuration layer to construct Policy/Profile.
Do not accept caller-supplied approval objects as authorization at a public API.
internal/app now supplies persistent reviewed-context previews and durable
read-only assessment jobs. Its DB-only AIConfigurationResolver still returns
point-in-time configuration, not an execution reservation. AssessmentWorker
rechecks current writer/profile/policy/grant authority before I/O, while waiting
and at a fenced commit. It keeps exact stored destination/revision bindings,
even when this adapter appends its native route to a reviewed base.

That worker requires an explicit direct verified-TLS client with copied trust
roots, no proxy/cookie/redirect bypass and bounded deadlines. A committed
dispatch marker precedes MaxAttempts=1. One durable explicit scope coordinates
concurrency and requests in any trailing configured window across profiles and
replicas. Cancelled, rejected or lost marked attempts still consume admission;
expired dispatches are uncertain, not automatically resubmitted.

For assessments only, the worker removes GetBody from its per-dispatch request
copy before calling the inner Transport. This prevents HTTP/2 REFUSED_STREAM
body replay inside one outer RoundTrip; it does not disable HTTP/2, change the
body/headers or alter standalone provider clients.

Concurrency is a separate durable attempt reservation, not a count of public
dispatching states. Cancellation or lease settlement cannot release it while
the original local I/O is live. Owner/token acknowledgment follows actual body,
socket and pending-dial cleanup. A fixed deadline starts before marker SQL and
bounds the attempt's private transport, including late dials and resumed I/O;
no new relative timeout starts after a pause. The database reserves the same
bounded budget for dead-owner recovery. Context-honoring dial hooks and normally
progressing clocks are required. Neither a disconnect nor deadline recovery
proves remote model termination or billing. No request-window charge is refunded.

Only the exact operator-reviewed derived context, its server ID/digest and
these fixed instructions are sent. It is not original scanner-byte proof, and
later scans do not replace that snapshot. Operator review is not a universal
credential detector. Known credential echoes and oversized advisory/native
metadata are rejected or omitted before persistence. Neither a supported nor
an inconclusive advisory changes finding workflow, owner, risk or source state.
No shell, model tool, scanner, target probe or verification execution is enabled.

Account/model/region token rates, costs, UI and runtime/deployment integration
remain separate work. Owned native fixtures are not live-provider, quality,
retention or geography verification; store=false is a request, not a universal
retention guarantee. Returned model text does not certify an immutable model.
Native Ollama API, streaming and fallback are not advertised as present.
