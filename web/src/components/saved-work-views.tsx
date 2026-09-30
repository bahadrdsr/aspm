import { useCallback, useId, useLayoutEffect, useRef, useState } from "react";
import { APIError } from "@/api/client";
import type { SavedWorkView } from "@/api/work-views";
import { useSavedWorkViews } from "@/lib/use-saved-work-views";
import type { SavedWorkViewsData } from "@/lib/use-saved-work-views";
import { useResource } from "@/lib/use-resource";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { EmptyState } from "./states";
import { Button } from "./ui/button";
import { DeleteWorkViewForm, EditWorkViewForm, SaveWorkViewForm } from "./saved-work-view-forms";
import type { WorkViewSnapshot } from "./saved-work-view-forms";
import "./saved-work-views.css";

export interface WorkViewApply {
  signal: AbortSignal;
  submit: (view: SavedWorkView) => void;
}
interface Intent {
  id: string; kind: "apply" | "edit" | "delete"; sequence: number; epoch: number;
  trigger: HTMLElement; apply?: WorkViewApply;
}
interface Props {
  snapshot: WorkViewSnapshot;
  beginApply: () => WorkViewApply;
  cancelApply: () => void;
}

function SavedViewAction({ intent, data, onClose }: { intent: Intent; data: SavedWorkViewsData; onClose: () => void }) {
  const load = useCallback(async (signal: AbortSignal) => {
    const scoped = intent.apply ? AbortSignal.any([signal, intent.apply.signal]) : signal;
    const view = await data.readView(intent.id, scoped);
    scoped.throwIfAborted();
    if (intent.kind === "apply") intent.apply?.submit(view);
    return view;
  }, [data.readView, intent]);
  const resource = useResource(load);
  const withheld = resource.status === "ready" && data.get(intent.id) === null;
  return <div className="saved-view-action">
    {resource.status === "loading" && <p role="status">Loading current authorized saved view detail. Confirmed Work results stay in place.</p>}
    <FormError error={resource.error ?? (withheld ? new APIError("Saved view metadata is withheld. Read its current authorized detail again.", "forbidden", false) : null)} />
    {(resource.error || withheld) && <div className="saved-view-buttons">
      <ActionButton variant="outline" onClick={resource.reload}>Retry saved view</ActionButton>
      <Button variant="ghost" onClick={onClose}>Cancel</Button>
    </div>}
    {resource.status === "ready" && resource.data && !withheld && intent.kind === "edit" &&
      <EditWorkViewForm key={resource.data.revision} view={resource.data} data={data} onClose={onClose} onReload={resource.reload} />}
    {resource.status === "ready" && resource.data && !withheld && intent.kind === "delete" &&
      <DeleteWorkViewForm key={resource.data.revision} view={resource.data} data={data} onClose={onClose} onReload={resource.reload} />}
    {resource.status === "ready" && !withheld && intent.kind === "apply" &&
      <p className="form-help">Authorized query snapshot sent through Work search. Only a successful Work read confirms findings and loaded sort.</p>}
  </div>;
}

function SavedViewsPanel({ id, snapshot, beginApply, cancelApply }: Props & { id: string }) {
  const data = useSavedWorkViews();
  const [intent, setIntent] = useState<Intent | null>(null);
  const [saving, setSaving] = useState<{ snapshot: WorkViewSnapshot; epoch: number } | null>(null);
  const nextIntent = useRef(0), saveTrigger = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    setIntent(null);
    setSaving(null);
    cancelApply();
  }, [data.epoch]);
  useLayoutEffect(() => {
    if (!intent?.apply) return;
    const signal = intent.apply.signal;
    const discard = () => setIntent((previous) => previous === intent ? null : previous);
    if (signal.aborted) discard();
    else signal.addEventListener("abort", discard, { once: true });
    return () => signal.removeEventListener("abort", discard);
  }, [intent]);
  const closeForm = () => {
    const focus = intent?.trigger;
    setIntent(null);
    setSaving(null);
    if (focus?.isConnected) focus.focus();
    else saveTrigger.current?.focus();
  };
  const startAction = (kind: Intent["kind"], view: SavedWorkView, trigger: HTMLElement) => {
    cancelApply();
    setSaving(null);
    setIntent({
      id: view.id, kind, sequence: ++nextIntent.current, epoch: data.epoch, trigger,
      ...(kind === "apply" && { apply: beginApply() }),
    });
  };
  return <section id={id} className="surface saved-views-panel" aria-label="Saved views">
    <header className="saved-views-heading"><div><h2>Personal saved views</h2>
      <p>Your own query snapshots in the selected workspace.</p></div>
      <div className="saved-view-buttons"><ActionButton variant="outline" onClick={data.refresh} disabled={data.pending}>Refresh saved views</ActionButton>
        <Button ref={saveTrigger} variant="outline" disabled={data.page === null} onClick={() => {
          cancelApply(); setIntent(null); setSaving({ snapshot: { ...snapshot }, epoch: data.epoch });
        }}>Save current view</Button></div>
    </header>
    <div className="saved-views-content">
      <p className="form-help">A query snapshot is a personal preference, not saved results, additional access or a live subscription.
        Apply reads the current authorized template once, then uses the existing Work search. Loaded sorting is not global sorting.
        Later edits or deletion do not change the confirmed Work snapshot. Work does not bind its results atomically to a view revision.</p>
      {data.pending && <p role="status">Loading {data.page ? "saved views. Last authorized metadata stays in place." : "personal saved views."}</p>}
      <FormError error={data.error} />
      {data.error && <ActionButton variant="outline" disabled={data.pending} onClick={data.retry}>Retry saved views</ActionButton>}
      {saving?.epoch === data.epoch && <SaveWorkViewForm snapshot={saving.snapshot} data={data} onClose={closeForm} />}
      {intent?.epoch === data.epoch && <SavedViewAction key={intent.sequence} intent={intent} data={data} onClose={closeForm} />}
      <ul aria-label="Personal saved views" className="saved-views-list">
        {data.rows.map((view) => <li key={view.id}><div className="saved-view-label"><strong>{view.name}</strong>
          <span>Loaded sort: {view.sort}</span></div>
          <div className="saved-view-buttons">{(["apply", "edit", "delete"] as const).map((kind) =>
            <Button key={kind} variant="outline" size="sm" onClick={(event) => startAction(kind, view, event.currentTarget)}>
              {kind === "apply" ? "Apply" : kind === "edit" ? "Edit" : "Delete"}
            </Button>)}</div></li>)}
      </ul>
      {!data.pending && !data.error && data.page !== null && data.rows.length === 0 &&
        <EmptyState title="No saved views" description="Save a confirmed Work query and loaded sort as a personal preference." />}
      {data.page && <p className="form-help">{data.rows.length} loaded saved views; {data.page.total} total reported by the last list read.
        Pages and counts can change between reads.</p>}
      <ActionButton variant="outline" disabled={data.pending || data.page?.nextCursor == null} onClick={data.more}>Load more saved views</ActionButton>
    </div>
  </section>;
}

export function SavedWorkViews(props: Props) {
  const [open, setOpen] = useState(false);
  const panelId = useId(), toggle = useRef<HTMLButtonElement>(null);
  return <div className="saved-views-disclosure">
    <Button ref={toggle} variant="outline" aria-expanded={open} aria-controls={panelId} onClick={() => {
      if (open) props.cancelApply();
      setOpen((previous) => !previous);
      toggle.current?.focus();
    }}>Saved views</Button>
    {open && <SavedViewsPanel id={panelId} {...props} />}
  </div>;
}
