import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { teamsApi } from "@/api/teams";
import { sameTeamsMetadata } from "@/api/teams-input";
import {
  acknowledgeTeamsIntent, invalidateTeamsIntent, pendingTeamsIntent, sameTeamsIntent, teamsFindingIdentity, teamsQueueIntent,
} from "@/api/teams-intents";
import type { TeamsIntent } from "@/api/teams-intents";
import type { TeamsConnection, TeamsDeliveryResponse } from "@/api/teams-types";
import type { FindingDetail } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { TeamsPayloadView, TeamsTargetFacts } from "./teams-facts";
import { LoadingState } from "./states";
import { Button } from "./ui/button";

interface PreviewRead { connection: TeamsConnection; finding: FindingDetail; sequence: number }
export function TeamsNotificationReview({ finding, current, onClose, onReceipt }: {
  finding: FindingDetail; current: boolean; onClose: () => void;
  onReceipt: (response: TeamsDeliveryResponse, replay: boolean) => void;
}) {
  const { session, workspace } = useSession();
  const region = useRef<HTMLElement>(null), pending = useRef<HTMLParagraphElement>(null);
  const firstEntry = useRef(true), sequence = useRef(0);
  const [cursor, setCursor] = useState<string | null>(null), [selectedId, setSelectedId] = useState("");
  const [ticket, setTicket] = useState<PreviewRead | null>(null);
  const [intent, setIntent] = useState<TeamsIntent | null>(() => pendingTeamsIntent(finding.id));
  const [reason, setReason] = useState<string | null>(null);
  const [denied, setDenied] = useState(false), [acknowledged, setAcknowledged] = useState(false);
  const action = useScopedAction();
  const load = useCallback((signal: AbortSignal) => teamsApi.connections(cursor, signal), [cursor]);
  const connections = useResource(load), page = connections.data;
  const selected = connections.status === "ready" ? page?.items.find((item) => item.id === selectedId) : undefined;
  const loadPreview = useCallback(async (signal: AbortSignal) => {
    await Promise.resolve(); signal.throwIfAborted();
    if (ticket === null) return null;
    return { preview: await teamsApi.preview(ticket.finding, ticket.connection, session.user.id, signal), sequence: ticket.sequence };
  }, [ticket, session.user.id]);
  const local = useResource(loadPreview);
  const value = local.status === "ready" && ticket && local.data?.sequence === ticket.sequence &&
    selected?.enabled && selected.credentialConfigured && selected.revision === ticket.connection.revision &&
    selected.name === ticket.connection.name && sameTeamsMetadata(selected.teams, ticket.connection.teams) &&
    teamsFindingIdentity(finding) === teamsFindingIdentity(ticket.finding) ? local.data.preview : null;
  const canQueue = current && workspace.role !== "viewer" && !denied && !acknowledged && !action.pending &&
    connections.status === "ready" && value !== null && (intent === null || sameTeamsIntent(intent, value, finding));
  useLayoutEffect(() => { region.current?.focus({ preventScroll: true }); }, []);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  useLayoutEffect(() => {
    if (!current) { setTicket(null); invalidateTeamsIntent(finding.id);
      setReason("Finding details are being checked. A fresh authorized local preview and review are required."); }
  }, [current, finding.id]);
  useLayoutEffect(() => {
    if (!firstEntry.current || !current || connections.status !== "ready" || !page) return;
    firstEntry.current = false;
    const eligible = page.items.filter((item) => item.enabled && item.credentialConfigured);
    if (cursor === null && page.nextCursor === null && page.total === page.items.length && eligible.length === 1) {
      setSelectedId(eligible[0].id); setTicket({ connection: eligible[0], finding, sequence: ++sequence.current });
    }
  }, [connections.status, page, cursor, finding, current]);
  useLayoutEffect(() => {
    if (!intent) return;
    if (intent.finding !== teamsFindingIdentity(finding) || intent.preview.requestedBy !== session.user.id ||
      selectedId !== "" && selectedId !== intent.preview.connectionId ||
      connections.status === "ready" && selectedId === intent.preview.connectionId &&
        (!selected?.enabled || !selected.credentialConfigured || selected.revision !== intent.preview.connectionRevision ||
          selected.name !== intent.preview.destination.name || !sameTeamsMetadata(selected.teams, intent.preview.destination))) {
      invalidateTeamsIntent(finding.id);
      setReason("The original unresolved intent is retained, but its finding, actor, selection or target changed. Consent is invalid; read authorized history, not a blind resend.");
    }
  }, [intent, finding, session.user.id, selectedId, selected, connections.status]);
  function prepare(connection: TeamsConnection) {
    if (!current || action.pending || connections.status !== "ready" || !connection.enabled || !connection.credentialConfigured || denied) return;
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
      const original = teamsQueueIntent(value, finding);
      setIntent(original);
      return { ...await teamsApi.enqueue(value, original.input, signal), intent: original };
    }, ({ response, replay, intent }) => {
      acknowledgeTeamsIntent(finding.id, intent); setIntent(null); setAcknowledged(true); onReceipt(response, replay);
    }, (error) => {
      if (["forbidden", "not-found", "conflict"].includes(error.code)) {
        invalidateTeamsIntent(finding.id); setTicket(null);
        setReason("Consent is invalid after the service response. The unresolved original intent is retained. Review fresh authorized metadata; no replacement send is permitted.");
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
  return <section ref={region} tabIndex={-1} className="teams-review" aria-label="Teams notification review" onKeyDown={trap}>
    <header className="teams-heading"><h3>Teams notification review</h3>
      <Button type="button" variant="outline" onClick={onClose}>Close Teams review</Button></header>
    <p className="form-help">Local canonical preview only; this does not invoke a Workflow or enqueue anything.
      A separate explicit queue confirmation is required. Standard-channel / Anyone Workflows require owner and co-owner
      continuity; ownership, permissions and channel delivery are not verified.</p>
    <div className="slack-actions"><ActionButton variant="outline" disabled={connections.status === "loading" || action.pending}
      onClick={connections.error ? connections.reload : refreshConnections}>
        {connections.error ? "Retry Teams connections" : "Refresh Teams connections"}</ActionButton>
      <ActionButton variant="outline" disabled={!current || !selected?.enabled || !selected.credentialConfigured || action.pending || denied}
        onClick={() => { if (selected) prepare(selected); }}>{ticket && local.error ? "Retry Teams preview" : "Refresh Teams preview"}</ActionButton></div>
    <FormError error={error} />
    {connections.status === "loading" && !page && <LoadingState label="Loading Teams destinations" />}
    {page && connections.status === "ready" && <>
      <label>Teams connection<select aria-label="Teams connection" value={selectedId} disabled={action.pending || denied}
        onChange={(event) => {
          const id = event.target.value; setSelectedId(id); setTicket(null); setAcknowledged(false); setReason(null);
          const connection = page.items.find((item) => item.id === id); if (connection) prepare(connection);
        }}><option value="">Select an enabled Teams connection</option>
        {page.items.map((item) => <option key={item.id} value={item.id} disabled={!item.enabled || !item.credentialConfigured}>
          {item.name}{!item.enabled ? " (disabled)" : ""}</option>)}</select></label>
      <p className="form-help">{page.total} Teams connections at the last native read; {page.items.length} in this page.
        {" "}{page.nextCursor || page.total !== page.items.length ? "Unloaded pages cannot prove a unique destination. Select explicitly; no automatic page drain." :
          "Ambiguous destinations require explicit selection."}</p>
      {page.nextCursor !== null && <ActionButton variant="outline" disabled={action.pending} onClick={() => {
        setSelectedId(""); setTicket(null); setCursor(page.nextCursor);
      }}>Next Teams connections</ActionButton>}
    </>}
    {selected && <TeamsTargetFacts target={{ name: selected.name, ...selected.teams }} revision={selected.revision} />}
    {ticket && local.status === "loading" && <p role="status" className="form-help">Loading the local canonical preview; queue consent is disabled until its binding is validated.</p>}
    {value && <><TeamsPayloadView payload={value.payload} /><p className="form-help">Native validation not run.
      This preview is local only, not ownership, permission or channel-delivery verification.</p></>}
    {reason && <p className="form-help">{reason}</p>}
    {intent && <p className="form-help">The original intent and key are retained in bounded scoped memory.
      It may already be queued or possibly sent. {intent.invalidated ? "Consent is invalid; no replacement key or blind resend is permitted." :
        "Only explicit confirmation of the same original intent can reuse its key. Closing/reopening or history reads never resend."}</p>}
    {acknowledged && <p className="form-help">The service acknowledged this queue intent. Its last received state appears below; queuing is not Workflow acceptance.</p>}
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Awaiting the queue acknowledgement, not a channel receipt. Closing cannot undo a sent request.</p>}
    <footer className="form-actions"><ActionButton disabled={!canQueue} onClick={confirm}>
      {intent !== null && !action.pending ? "Confirm same Teams notification" : "Queue Teams notification"}</ActionButton></footer>
  </section>;
}
