import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { slackApi } from "@/api/slack";
import { acknowledgeNotification, notificationIntent, unresolvedNotification } from "@/api/slack-intents";
import type { DataOrigin, FindingDeliveryResponse, WorkItem } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { SlackDeliveryDetail } from "./slack-delivery-detail";
import { SlackDeliveryHistory } from "./slack-delivery-history";
import type { DeliveryAcknowledgements } from "./slack-delivery-history";
import { DataNotice, LoadingState } from "./states";
import { Button } from "./ui/button";
import "./slack.css";

type NotificationFinding = Pick<WorkItem, "id" | "title" | "severity" | "assetName">;

function NotificationPreview({ finding, origin, returnFocus, onClose, onAcknowledged }: {
  finding: NotificationFinding; origin: DataOrigin; returnFocus: HTMLElement; onClose: () => void;
  onAcknowledged: (response: FindingDeliveryResponse) => void;
}) {
  const { session, workspace } = useSession();
  const [intent, setIntent] = useState(() => unresolvedNotification(finding.id));
  const [selectedId, setSelectedId] = useState(intent?.connectionId ?? "");
  const [cursor, setCursor] = useState<string | null>(null);
  const load = useCallback((signal: AbortSignal) => slackApi.connections(100, cursor, signal), [cursor]);
  const resource = useResource(load);
  const selected = resource.data?.items.find((item) => item.id === selectedId);
  const action = useScopedAction();
  const pending = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => {
    if (action.pending) pending.current?.focus({ preventScroll: true });
  }, [action.pending]);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your workspace role is read only. Notification history remains available.", "forbidden", false);
      if (!selected?.enabled || resource.status !== "ready") throw new APIError("Select an enabled connection from an authorized workspace response.", "invalid-input", false);
      const proposed = notificationIntent(finding.id, selected.id);
      setIntent(proposed);
      return { response: await slackApi.enqueue(finding.id, proposed, session.user.id, signal), intent: proposed };
    }, ({ response, intent }) => {
      acknowledgeNotification(finding.id, intent);
      onAcknowledged(response);
    });
  }
  return <FormDialog title="Send to Slack" containFocus returnFocus={returnFocus} onClose={onClose}
    description="Review a small outbound notification. Only explicit confirmation queues it with the service; opening or cancelling this preview sends nothing.">
    <form aria-label="Slack notification" className="application-form slack-form" onSubmit={submit} aria-busy={action.pending}>
      <DataNotice origin={origin} />
      <div className="slack-payload"><h3>{finding.title}</h3><p>{`Severity: ${finding.severity}\nAsset: ${finding.assetName}`}</p>
        <a className="slack-link" href={`${window.location.origin}/#/work?finding=${encodeURIComponent(finding.id)}`}>View finding</a>
      </div>
      <p className="form-help">Only the finding title, severity, asset and link are included. No original evidence, source code, notes or remediation text.
        {" "}The service takes the final immutable snapshot and uses its configured finding-link origin.</p>
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry connections</ActionButton>}
      {resource.status === "loading" && !resource.data && <LoadingState label="Loading connections" />}
      {resource.data && <>
        {resource.data.dataOrigin && <DataNotice origin={resource.data.dataOrigin} />}
        <fieldset className="form-grid" disabled={action.pending || resource.status !== "ready"}>
          <legend className="sr-only">Notification destination</legend>
          <label className="full-width">Connection<select required value={selectedId} disabled={intent !== null}
            onChange={(event) => setSelectedId(event.target.value)}>
            <option value="" disabled>Select an enabled connection</option>
            {resource.data.items.filter((item) => item.enabled).map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}
          </select></label>
          {selected && <p className="form-help full-width"><strong>{selected.name}</strong><br />Channel: <code>{selected.channel}</code><br />
            {selected.enabled ? "Enabled" : "Disabled"} · Revision: {selected.revision}. Stored credential metadata is not live verification.</p>}
          {!resource.data.items.some((item) => item.enabled) && <p className="form-help full-width">No enabled connections in this page.
            {" "}{resource.data.nextCursor ? "Read the next page to see more destinations." : "Ask an administrator to configure an enabled Slack destination."}</p>}
        </fieldset>
        {resource.data.nextCursor !== null && <ActionButton variant="outline" disabled={action.pending || resource.status === "loading"}
          onClick={() => { setCursor(resource.data!.nextCursor); if (!intent) setSelectedId(""); }}>Next connections</ActionButton>}
      </>}
      {intent && <p className="form-help">This notification intent is retained until its acknowledgement is confirmed. It may already have been queued.
        {" "}Another confirmation uses the same connection and idempotency key, never a fresh notification. Closing does not roll back a request.</p>}
      <FormError error={action.error} />
      {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Queuing with the service. Awaiting acknowledgement, not a Slack send receipt. Closing does not undo a request already sent.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || !selected?.enabled || resource.status !== "ready" || workspace.role === "viewer"}>
          {intent && !action.pending ? "Confirm same notification" : "Queue notification"}</ActionButton></footer>
    </form>
  </FormDialog>;
}

interface Selection { id: string; initial: FindingDeliveryResponse | null; generation: number }

export function FindingNotifications({ finding, origin }: { finding: NotificationFinding; origin: DataOrigin }) {
  const { workspace } = useSession();
  const [preview, setPreview] = useState<{ trigger: HTMLElement } | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [acknowledged, setAcknowledged] = useState<DeliveryAcknowledgements>({ revision: 0, items: new Map() });
  function select(id: string, initial: FindingDeliveryResponse | null = null) {
    setSelection((previous) => ({ id, initial, generation: (previous?.generation ?? 0) + 1 }));
  }
  function accept(response: FindingDeliveryResponse) {
    setAcknowledged((previous) => {
      const revision = previous.revision + 1;
      return { revision, items: new Map(previous.items).set(response.delivery.id, { revision, response }) };
    });
    setPreview(null);
    setHistoryOpen(true);
    select(response.delivery.id, response);
  }
  return <section className="slack-notifications" aria-label="Finding notifications">
    <div className="slack-notifications-heading"><h3>Notifications</h3>
      {workspace.role !== "viewer" && <ActionButton variant="outline" size="sm"
        onClick={(event) => setPreview({ trigger: event.currentTarget })}>Notify</ActionButton>}
      <Button type="button" variant="ghost" size="sm" aria-expanded={historyOpen} onClick={() => setHistoryOpen((value) => !value)}>Delivery history</Button>
    </div>
    <p className="form-help">An explicit Slack notification shares a small finding summary. It does not change the finding or verify its source.</p>
    {selection && <SlackDeliveryDetail key={selection.generation} id={selection.id} findingId={finding.id} initial={selection.initial} />}
    {historyOpen && <SlackDeliveryHistory findingId={finding.id} acknowledged={acknowledged} onSelect={select} />}
    {workspace.role !== "viewer" && preview && <NotificationPreview finding={finding} origin={origin} returnFocus={preview.trigger}
      onClose={() => setPreview(null)} onAcknowledged={accept} />}
  </section>;
}
