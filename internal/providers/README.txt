M11 provider adapters
====================
Open constructs a no-I/O, immutable profile/policy snapshot. Assess enforces
workspace/task/data-class approval and exact configured destination before
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

All calls use the supplied HTTP client transport with a cloned client that
does not follow redirects or forward ambient cookie jars. No automatic retry
or fallback is implemented; MaxAttempts is a ceiling, not a request to repeat.
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
Application persistence, shared rate/token admission, job orchestration and UI
are separate integration work. The current HTTP profiles are deliberately
narrow; native Ollama API, streaming and fallback are not advertised as present.
