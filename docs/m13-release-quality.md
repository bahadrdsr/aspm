# M13 technical preview quality and capacity

Qualified on October 10, 2026.

The machine-readable evidence is
`docs\evidence\m13-technical-preview-quality.json`.

## Accessibility

The focused Chromium/Playwright gate checks:

- accessible names for exposed controls and headings;
- keyboard-only traversal and visible focus;
- automated text contrast;
- 24 px minimum form-control targets;
- 200 percent equivalent narrow-viewport reflow without page overflow;
- reduced-motion behavior;
- text/status communication that is not color-only.

The audit corrected one sidebar contrast token and enlarged checkbox targets.
This is an automated WCAG 2.2 AA subset, not a complete manual audit, screen
reader certification, or formal conformance claim.

## Browser responsiveness

The fixture-backed cold interactive shell must settle within 2 seconds. Local
filter feedback is measured inside Chromium from the native input event through
the next animation frame, with p95 bounded at 100 ms. Cross-process Playwright
control latency is deliberately excluded from that local-feedback measurement.

## Native capacity profile

The signed native release accepted one 1,514,183-byte SARIF containing 5,000
unique findings through the public API. The independent ingestion worker
completed it in 4.54 seconds. The first 100-row work page reported all 5,000
findings, with 40 measured requests at:

- p95: 56.42 ms;
- p99: 57.46 ms.

Upload acknowledgement was 82.38 ms. No acknowledged finding was lost. Maximum
observed container memory was 95.85 MiB; every container remained below the
existing 512 MiB streaming-worker budget.

This qualifies the bounded `technical-preview-5000` profile only. It does not
qualify the existing enterprise E1 hypotheses, one-million-finding interactive
behavior, one-gigabyte report ingestion, multi-tenant noisy-neighbor isolation,
stateful HA, or long-duration soak behavior.
