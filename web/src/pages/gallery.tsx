import { useState } from "react";
import { Button } from "@/components/ui/button";
import { ActionButton } from "@/components/action-button";
import { Icon } from "@/components/icon";
import type { IconName } from "@/components/icon";
import { SeverityBadge, WorkflowBadge } from "@/components/states";
import { usePreferences } from "@/lib/preferences";

const states: { name: string; icon: IconName; tone: string; description: string; detail: string }[] = [
  { name: "Empty", icon: "work", tone: "neutral", description: "Nothing in this view yet.", detail: "Offer a useful next step, not sample findings." },
  { name: "Loading", icon: "clock", tone: "indigo", description: "Waiting for the data service.", detail: "Keep layout stable. Never invent progress." },
  { name: "Error", icon: "warning", tone: "danger", description: "The request could not complete.", detail: "Keep the cause and recovery action visible." },
  { name: "Permission denied", icon: "lock", tone: "neutral", description: "This scope is not accessible.", detail: "Explain access without exposing its records." },
  { name: "Partial data", icon: "layers", tone: "amber", description: "Some source coverage is missing.", detail: "Show what is missing, not a healthy total." },
  { name: "Success", icon: "check", tone: "success", description: "The example action is complete.", detail: "Confirm only what actually happened." },
];

export function GalleryPage() {
  const { reducedMotion, theme } = usePreferences();
  const [feedback, setFeedback] = useState(false);
  return <>
    <header className="page-heading gallery-heading"><div><p className="eyebrow">Design system / M01</p><h1>Synthetic component gallery</h1><p className="page-description">A quiet, consistent foundation for complex security work.</p></div><span className="subtle-pill">Independent review pending</span></header>
    <aside className="synthetic-note" role="note" aria-label="Synthetic data notice"><span className="note-icon"><Icon name="grid" size={19} /></span><div><strong>Deliberately synthetic.</strong><p>These are isolated component examples, not live data or verification. They never replace a failed API response.</p></div></aside>
    <section className="surface control-gallery" aria-labelledby="control-heading">
      <div className="gallery-section-label"><h2 id="control-heading">Shared controls</h2><span>Reviewed source. Owned composition.</span></div>
      <div className="control-examples"><div><span className="sample-label">Actions</span><div className="example-row"><ActionButton onClick={() => setFeedback(true)}><Icon name="check" />Try interaction</ActionButton><Button variant="outline" onClick={() => setFeedback(false)}>Reset example</Button><Button variant="secondary" disabled>Unavailable</Button></div></div><div><span className="sample-label">Meaning, not just color</span><div className="example-row"><SeverityBadge severity="high" /><SeverityBadge severity="medium" /><WorkflowBadge value="in-progress" /></div></div></div>
      <p className="gallery-feedback" role="status">{feedback ? "Synthetic example complete. No data was changed or sent." : "The primary action uses the approved Animate UI Button; secondary controls use shadcn/ui."}</p>
    </section>
    <section className="state-samples" aria-labelledby="state-samples-heading">
      <div className="section-heading"><h2 id="state-samples-heading">State samples</h2><span className="muted">Different outcomes. Clear next steps.</span></div>
      <div className="state-grid">{states.map((state) => <article className={`state-card tone-${state.tone}`} key={state.name}><div className="state-card-heading"><span className="state-icon"><Icon name={state.icon} size={20} /></span><span className="sample-tag">Example</span></div><h3>{state.name}</h3><p>{state.description}</p><span className="state-detail">{state.detail}</span>{state.name === "Loading" && <div className="sample-loading-bar" aria-hidden="true"><span /></div>}</article>)}</div>
    </section>
    <section className="surface token-gallery" aria-labelledby="token-heading"><div><h2 id="token-heading">One visual language</h2><p>System typography. Indigo intent. Neutral surfaces. No remote fonts.</p></div><div className="swatches" aria-label="Theme color samples"><span className="swatch swatch-ink" title="Text" /><span className="swatch swatch-muted" title="Muted text" /><span className="swatch swatch-primary" title="Primary" /><span className="swatch swatch-soft" title="Accent" /><span className="swatch swatch-surface" title="Surface" /></div><dl><div><dt>Theme</dt><dd>{theme === "dark" ? "Dark" : "Light"}</dd></div><div><dt>Motion</dt><dd>{reducedMotion ? "Reduced by system" : "100 / 180 / 240 ms"}</dd></div><div><dt>Primitives</dt><dd>Radix + native HTML</dd></div></dl></section>
    <p className="view-footnote"><Icon name="shield" size={15} />A component gallery is not a WCAG conformance claim. Keyboard, responsive, reduced-motion and visual review remain separate gates.</p>
  </>;
}
