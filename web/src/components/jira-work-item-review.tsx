import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { jiraApi } from "@/api/jira";
import { sameJiraTarget } from "@/api/jira-input";
import {
  acknowledgeJiraIntent, invalidateJiraIntent, jiraFindingIdentity, jiraQueueIntent, pendingJiraIntent, sameJiraIntent,
} from "@/api/jira-intents";
import type { JiraIntent } from "@/api/jira-intents";
import type { JiraConnection, JiraDeliveryResponse } from "@/api/jira-types";
import type { FindingDetail } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { JiraPayloadView, JiraTargetFacts } from "./jira-facts";
import { LoadingState } from "./states";
import { Button } from "./ui/button";

interface PreviewRead { connection: JiraConnection; finding: FindingDetail; sequence: number }

export function JiraWorkItemReview({ finding, current, onClose, onReceipt }: {
  finding: FindingDetail; current: boolean; onClose: () => void; onReceipt: (response: JiraDeliveryResponse, replay: boolean) => void;
}) {
  const { session, workspace } = useSession();
  const region = useRef<HTMLElement>(null), pending = useRef<HTMLParagraphElement>(null);
  const firstEntry = useRef(true), sequence = useRef(0);
  const [cursor, setCursor] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState("");
  const [ticket, setTicket] = useState<PreviewRead | null>(null);
  const [intent, setIntent] = useState<JiraIntent | null>(() => pendingJiraIntent(finding.id));
  const [reason, setReason] = useState<string | null>(null);
  const [denied, setDenied] = useState(false), [acknowledged, setAcknowledged] = useState(false);
  const action = useScopedAction();
  const load = useCallback((signal: AbortSignal) => jiraApi.connections(cursor, signal), [cursor]);
  const connections = useResource(load), page = connections.data;
  const selected = connections.status === "ready" ? page?.items.find((item) => item.id === selectedId) : undefined;
  const loadPreview = useCallback(async (signal: AbortSignal) => {
    await Promise.resolve();
    signal.throwIfAborted();
    if (ticket === null) return null;
    const preview = await jiraApi.preview(ticket.finding, ticket.connection, session.user.id, signal);
    return { preview, sequence: ticket.sequence };
  }, [ticket, session.user.id]);
  const local = useResource(loadPreview);
  const value = local.status === "ready" && ticket && local.data?.sequence === ticket.sequence &&
    selected?.enabled && selected.credentialConfigured && selected.revision === ticket.connection.revision &&
    sameJiraTarget(selected.jira, ticket.connection.jira) &&
    jiraFindingIdentity(finding) === jiraFindingIdentity(ticket.finding) ? local.data.preview : null;
  const replay = intent !== null && !action.pending;
  const validIntent = intent === null || value !== null && sameJiraIntent(intent, value, finding);
  const canQueue = current && workspace.role !== "viewer" && !denied && !acknowledged && !action.pending &&
    connections.status === "ready" && value !== null && validIntent;
  useLayoutEffect(() => { region.current?.focus({ preventScroll: true }); }, []);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  useLayoutEffect(() => {
    if (!current) {
      setTicket(null);
      invalidateJiraIntent(finding.id);
      setReason("Finding details are stale or being checked. A fresh authorized local preview and review are required; no old consent can queue.");
    }
  }, [current, finding.id]);
  useLayoutEffect(() => {
    if (!firstEntry.current || !current || connections.status !== "ready" || !page) return;
    firstEntry.current = false;
    const enabled = page.items.filter((item) => item.enabled);
    if (cursor === null && page.nextCursor === null && page.total === page.items.length &&
      enabled.length === 1 && enabled[0].credentialConfigured) {
      setSelectedId(enabled[0].id);
      setTicket({ connection: enabled[0], finding, sequence: ++sequence.current });
    }
  }, [connections.status, page, cursor, finding, current]);
  useLayoutEffect(() => {
    if (!intent) return;
    if (intent.finding !== jiraFindingIdentity(finding) || intent.preview.requestedBy !== session.user.id ||
      selectedId !== "" && selectedId !== intent.preview.connectionId ||
      connections.status === "ready" && selectedId === intent.preview.connectionId &&
        (!selected?.enabled || !selected.credentialConfigured || selected.revision !== intent.preview.connectionRevision ||
          !sameJiraTarget(selected.jira, intent.preview.jira))) {
      invalidateJiraIntent(finding.id);
      setReason("The original unresolved intent is retained, but its finding, selection or target changed. Its consent is invalid. Read authorized history; do not blindly resend.");
    }
  }, [intent, finding, session.user.id, selectedId, selected, connections.status]);
  function prepare(connection: JiraConnection) {
    if (!current || action.pending || connections.status !== "ready" || !connection.enabled || !connection.credentialConfigured) return;
    setAcknowledged(false); setReason(null);
    setTicket({ connection, finding, sequence: ++sequence.current });
  }
  function refreshConnections() {
    setTicket(null); setAcknowledged(false);
    setReason("Connection metadata is being read again. Review a fresh local preview before confirming.");
    if (cursor !== null) setCursor(null); else connections.reload();
  }
  function confirm() {
    if (!canQueue || !value) return;
    void action.run(async (signal) => {
      const original = jiraQueueIntent(value, finding);
      setIntent(original);
      return { ...await jiraApi.enqueue(value, original.input, signal), intent: original };
    }, ({ response, replay, intent }) => {
      acknowledgeJiraIntent(finding.id, intent);
      setIntent(null); setAcknowledged(true); onReceipt(response, replay);
    }, (error) => {
      if (["forbidden", "not-found", "conflict"].includes(error.code)) {
        invalidateJiraIntent(finding.id); setTicket(null);
        setReason("Consent is invalid after the service response. A fresh authorized preview and review are required; the unresolved original intent has not been discarded.");
        if (error.code !== "conflict") setDenied(true);
      }
    });
  }
  function trap(event: KeyboardEvent<HTMLElement>) {
    if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); onClose(); return; }
    if (event.key !== "Tab") return;
    event.stopPropagation();
    const controls = [...event.currentTarget.querySelectorAll<HTMLElement>("button, select, a[href], [tabindex]")]
      .filter((item) => item.tabIndex >= 0 && !item.matches(":disabled") && item.getClientRects().length > 0);
    const first = controls[0], last = controls.at(-1);
    if (first && last && (event.shiftKey ? document.activeElement === first : document.activeElement === last)) {
      event.preventDefault(); (event.shiftKey ? last : first).focus();
    }
  }
  const error = connections.error ?? (ticket ? local.error : null) ?? action.error;
  return <section ref={region} tabIndex={-1} className="jira-review" aria-label="Jira work item review" onKeyDown={trap}>
    <header className="jira-work-items-heading"><h3>Jira work item review</h3>
      <Button type="button" variant="outline" onClick={onClose}>Close Jira review</Button></header>
    <p className="form-help">Local server preview only. Opening this review does not create an issue. Queueing requires a separate explicit confirmation.</p>
    <div className="slack-actions"><ActionButton variant="outline" disabled={connections.status === "loading" || action.pending}
      onClick={refreshConnections}>{connections.error ? "Retry Jira connections" : "Refresh Jira connections"}</ActionButton>
      <ActionButton variant="outline" disabled={!current || !selected?.enabled || !selected.credentialConfigured || action.pending || denied}
        onClick={() => { if (selected) prepare(selected); }}>{ticket && local.error ? "Retry Jira preview" : "Refresh Jira preview"}</ActionButton></div>
    <FormError error={error} />
    {connections.status === "loading" && !page && <LoadingState label="Loading Jira destinations" />}
    {page && connections.status === "ready" && <>
      <label>Jira connection<select value={selectedId} disabled={action.pending || denied}
        onChange={(event) => {
          const id = event.target.value;
          setSelectedId(id); setTicket(null); setAcknowledged(false); setReason(null);
          const connection = page.items.find((item) => item.id === id);
          if (connection) prepare(connection);
        }}><option value="">Select an enabled Jira connection</option>
        {page.items.map((item) => <option key={item.id} value={item.id} disabled={!item.enabled || !item.credentialConfigured}>{item.name}{!item.enabled ? " (disabled)" : ""}</option>)}
      </select></label>
      <p className="form-help">{page.total} Jira connections at the last native read; {page.items.length} loaded in this page.
        {" "}{page.nextCursor || page.total !== page.items.length ? "Unloaded pages are not proof of a single enabled destination. Select explicitly; no pages are automatically drained." :
          "Multiple enabled destinations require an explicit selection before preview."}</p>
      {page.nextCursor !== null && <ActionButton variant="outline" disabled={action.pending} onClick={() => {
        setSelectedId(""); setTicket(null); setCursor(page.nextCursor);
      }}>Next Jira connections</ActionButton>}
    </>}
    {selected && <JiraTargetFacts target={selected.jira} revision={selected.revision} />}
    {ticket && local.status === "loading" && <p role="status" className="form-help">Loading the local canonical preview. Queue consent is disabled until its binding is validated.</p>}
    {value && <>
      <JiraPayloadView payload={value.payload} />
      <p className="form-help">Native validation not run. The worker checks native required fields and Jira permissions before creation.</p>
    </>}
    {reason && <p className="form-help">{reason}</p>}
    {selected && !selected.enabled && <p className="form-help">This destination is disabled or changed. Review a new preview after an authorized enabled configuration is received.</p>}
    {intent && <p className="form-help">The original intent and key are retained in bounded scoped memory. It may already be queued or possibly created.
      {" "}{intent.invalidated ? "Old consent is invalid; no blind resend or replacement intent is permitted." :
        "Only explicit confirmation of the same intent can use the original key. Closing or reopening does not resend or roll back anything."}</p>}
    {acknowledged && <p className="form-help">The service acknowledged this intent. Its receipt is shown below. A queued receipt is not Jira creation.</p>}
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Awaiting the queue acknowledgement, not a native creation receipt. Closing cannot undo a sent request.</p>}
    <footer className="form-actions"><ActionButton disabled={!canQueue} onClick={confirm}>
      {replay ? "Confirm same Jira work item" : "Queue Jira work item"}</ActionButton></footer>
  </section>;
}
