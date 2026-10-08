import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { webhookApi } from "@/api/generic-webhooks";
import type {
  WebhookDelivery, WebhookDeliveryResponse, WebhookPreview,
} from "@/api/generic-webhooks";
import type { FindingDetail } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { LoadingState } from "./states";
import { Button } from "./ui/button";
import "./slack.css";
import "./webhook.css";

function WebhookReview({ preview, returnFocus, onClose, onQueued }: {
  preview: WebhookPreview; returnFocus: HTMLElement;
  onClose: () => void; onQueued: (response: WebhookDeliveryResponse) => void;
}) {
  const action = useScopedAction();
  const intent = useRef(crypto.randomUUID());
  return <FormDialog title="Review webhook" containFocus returnFocus={returnFocus} onClose={onClose}
    description="Review the fixed signed payload before explicitly queueing it.">
    <section className="webhook-review" aria-label="Webhook review">
      <dl className="detail-facts">
        <div><dt>Origin</dt><dd>{preview.webhook.origin}</dd></div>
        <div><dt>Path</dt><dd><code>{preview.webhook.path}</code></dd></div>
        <div><dt>Signature</dt><dd>hmac-sha256</dd></div>
        <div className="full-width"><dt>Title</dt><dd>{preview.payload.title}</dd></div>
        <div className="full-width"><dt>Body</dt><dd>{preview.payload.body}</dd></div>
      </dl>
      <p className="form-help">Explicit queue consent · Operator-approved origin · Receiver signature verification.</p>
      <p className="form-help">No evidence, notes, remediation, unmapped fields, arbitrary headers or templates are included.</p>
      <FormError error={action.error} />
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton disabled={action.pending} onClick={() => {
          void action.run((signal) => webhookApi.enqueue(preview, intent.current, signal),
            ({ response }) => onQueued(response));
        }}>Queue webhook</ActionButton></footer>
    </section>
  </FormDialog>;
}

function WebhookChooser({ finding, current, onPreview }: {
  finding: FindingDetail; current: boolean;
  onPreview: (preview: WebhookPreview, trigger: HTMLElement) => void;
}) {
  const { session, workspace } = useSession();
  const [selectedId, setSelectedId] = useState("");
  const load = useCallback((signal: AbortSignal) => webhookApi.connections(null, signal), []);
  const resource = useResource(load), action = useScopedAction();
  const selected = resource.data?.items.find((item) => item.id === selectedId);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trigger = event.currentTarget.querySelector<HTMLButtonElement>('button[type="submit"]');
    if (!trigger) return;
    void action.run(async (signal) => {
      if (!current || workspace.role === "viewer") throw new APIError(
        "A current writable finding is required.", "forbidden", false);
      if (!selected?.enabled) throw new APIError("Select an enabled webhook connection.", "invalid-input", false);
      return webhookApi.preview(finding, selected, session.user.id, signal);
    }, (preview) => onPreview(preview, trigger));
  }
  return <form className="application-form webhook-send-form" aria-label="Send webhook" onSubmit={submit}>
    <FormError error={resource.error ?? action.error} />
    {resource.status === "loading" && !resource.data && <LoadingState label="Loading webhook connections" />}
    {resource.data && <label>Webhook connection<select value={selectedId} required
      onChange={(event) => setSelectedId(event.target.value)}>
      <option value="">Select an enabled webhook connection</option>
      {resource.data.items.map((item) => <option key={item.id} value={item.id}
        disabled={!item.enabled || !item.credentialConfigured}>{item.name}</option>)}
    </select></label>}
    <ActionButton type="submit" disabled={!selected?.enabled || action.pending || !current}>Review webhook</ActionButton>
  </form>;
}

function WebhookDeliveryDetails({ id, findingId, initial }: {
  id: string; findingId: string; initial: WebhookDelivery | null;
}) {
  const action = useScopedAction();
  const [delivery, setDelivery] = useState(initial);
  useEffect(() => {
    if (initial) return;
    void action.run((signal) => webhookApi.delivery(id, findingId, signal),
      (response) => setDelivery(response.delivery));
  // The delivery identity owns this read.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, findingId]);
  return <section className="surface slack-panel webhook-delivery" aria-label="Webhook delivery details">
    <header className="slack-panel-heading"><h3>Webhook delivery details</h3>
      <ActionButton variant="outline" onClick={() => void action.run(
        (signal) => webhookApi.delivery(id, findingId, signal),
        (response) => setDelivery(response.delivery),
      )}>Refresh webhook delivery</ActionButton></header>
    <FormError error={action.error} />
    {delivery && <div className="slack-panel-content"><p><code>{delivery.id}</code></p>
      <p>{delivery.webhook.origin}<code>{delivery.webhook.path}</code></p>
      <p>{delivery.state === "accepted"
        ? "Endpoint accepted; downstream processing not confirmed."
        : delivery.state === "uncertain" ? "Delivery outcome is uncertain. Inspect the receiver before another action."
        : label(delivery.state)}</p>
      <p>Created <time dateTime={delivery.createdAt}>{timestampLabel(delivery.createdAt)}</time></p>
      {delivery.failure && <p>Failure: {delivery.failure.code} · HTTP {delivery.failure.httpStatus}</p>}
    </div>}
  </section>;
}

function WebhookHistory({ findingId, onSelect }: {
  findingId: string; onSelect: (delivery: WebhookDelivery) => void;
}) {
  const [cursor, setCursor] = useState<string | null>(null);
  const load = useCallback((signal: AbortSignal) => webhookApi.history(findingId, cursor, signal), [findingId, cursor]);
  const resource = useResource(load), page = resource.data;
  return <section className="surface slack-panel webhook-history" aria-label="Webhook history">
    <header className="slack-panel-heading"><h3>Webhook history</h3>
      <ActionButton variant="outline" onClick={resource.reload}>Refresh webhook history</ActionButton></header>
    <FormError error={resource.error} />
    {page && <div className="slack-panel-content"><table className="slack-table" aria-label="Webhook deliveries">
      <thead><tr><th scope="col">Delivery</th><th scope="col">Action</th></tr></thead>
      <tbody>{page.items.map((item) => <tr key={item.id}><td><code>{item.id}</code><p>{label(item.state)}</p></td>
        <td><Button type="button" variant="outline" size="sm"
          onClick={() => onSelect(item)}>View webhook delivery</Button></td></tr>)}</tbody>
    </table>
      {page.nextCursor && <ActionButton variant="outline"
        onClick={() => setCursor(page.nextCursor)}>Next webhook deliveries</ActionButton>}
    </div>}
  </section>;
}

export function FindingWebhooks({ finding, current }: { finding: FindingDetail; current: boolean }) {
  const { workspace } = useSession();
  const [sendOpen, setSendOpen] = useState(false), [historyOpen, setHistoryOpen] = useState(false);
  const [review, setReview] = useState<{ preview: WebhookPreview; trigger: HTMLElement } | null>(null);
  const [selection, setSelection] = useState<{ id: string; initial: WebhookDelivery | null } | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  return <section className="webhook-finding" aria-label="Finding webhooks">
    <header className="teams-heading"><h3>Webhooks</h3>
      {workspace.role !== "viewer" && <Button type="button" variant="outline" size="sm"
        disabled={!current} onClick={() => setSendOpen((value) => !value)}>Send webhook</Button>}
      <Button type="button" variant="ghost" size="sm"
        onClick={() => setHistoryOpen((value) => !value)}>Webhook history</Button></header>
    <p className="form-help">Fixed HMAC-signed JSON only. Queueing is not receiver processing confirmation.</p>
    {message && <p role="status" className="form-help">{message}</p>}
    {sendOpen && workspace.role !== "viewer" && <WebhookChooser finding={finding} current={current}
      onPreview={(preview, trigger) => setReview({ preview, trigger })} />}
    {review && <WebhookReview preview={review.preview} returnFocus={review.trigger}
      onClose={() => setReview(null)} onQueued={(response) => {
        setReview(null); setSendOpen(false);
        setSelection({ id: response.delivery.id, initial: response.delivery });
        setMessage("Webhook queued, not sent. Read delivery state separately.");
      }} />}
    {selection && <WebhookDeliveryDetails id={selection.id} findingId={finding.id} initial={selection.initial} />}
    {historyOpen && <WebhookHistory findingId={finding.id}
      onSelect={(delivery) => setSelection({ id: delivery.id, initial: null })} />}
  </section>;
}
