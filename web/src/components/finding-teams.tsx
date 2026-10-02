import { useLayoutEffect, useRef, useState } from "react";
import type { FindingDetail } from "@/api/types";
import type { TeamsDeliveryResponse } from "@/api/teams-types";
import { invalidateTeamsIntent, teamsFindingIdentity } from "@/api/teams-intents";
import { useSession } from "@/lib/session";
import { Button } from "./ui/button";
import { TeamsDeliveryDetail } from "./teams-delivery-detail";
import { TeamsDeliveryHistory } from "./teams-delivery-history";
import { TeamsNotificationReview } from "./teams-notification-review";
import "./slack.css";
import "./teams.css";

export function FindingTeams({ finding, current }: { finding: FindingDetail; current: boolean }) {
  const { workspace } = useSession();
  const section = useRef<HTMLElement>(null), entry = useRef<HTMLButtonElement>(null);
  const [reviewOpen, setReviewOpen] = useState(false), [historyOpen, setHistoryOpen] = useState(false);
  const [selection, setSelection] = useState<{ id: string; initial: TeamsDeliveryResponse | null; replay: boolean; sequence: number } | null>(null);
  const identity = teamsFindingIdentity(finding), previous = useRef(identity);
  useLayoutEffect(() => {
    if (previous.current !== identity) {
      const active = document.activeElement;
      const restore = active !== null && section.current?.contains(active) &&
        active.closest('[aria-label="Teams notification review"], [aria-label="Teams notification details"]') !== null;
      invalidateTeamsIntent(finding.id); setReviewOpen(false); setSelection(null); previous.current = identity;
      if (restore) entry.current?.focus({ preventScroll: true });
    }
  }, [identity, finding.id]);
  function close() {
    const active = document.activeElement;
    const restore = active !== null && section.current?.contains(active);
    setReviewOpen(false); if (restore) entry.current?.focus({ preventScroll: true });
  }
  function select(id: string, initial: TeamsDeliveryResponse | null = null, replay = false) {
    setSelection((previous) => ({ id, initial, replay, sequence: (previous?.sequence ?? 0) + 1 }));
  }
  return <section ref={section} className="teams-notifications" aria-label="Finding Teams notifications">
    <header className="teams-heading"><h3>Teams notifications</h3>
      {workspace.role !== "viewer" && <Button ref={entry} type="button" variant="outline" size="sm"
        aria-expanded={reviewOpen} disabled={!current} onClick={() => setReviewOpen(true)}>Notify Teams</Button>}
      <Button type="button" variant="ghost" size="sm" aria-expanded={historyOpen}
        onClick={() => setHistoryOpen((value) => !value)}>Teams notification history</Button></header>
    <p className="form-help">Explicit local review and manual history only. No finding changes, vendor permissions or channel receipts are inferred.</p>
    {reviewOpen && workspace.role !== "viewer" && <TeamsNotificationReview finding={finding} current={current} onClose={close}
      onReceipt={(response, replay) => select(response.delivery.id, response, replay)} />}
    {selection && <TeamsDeliveryDetail key={selection.sequence} id={selection.id} findingId={finding.id}
      initial={selection.initial} replay={selection.replay} />}
    {historyOpen && <TeamsDeliveryHistory findingId={finding.id} onSelect={select} />}
  </section>;
}
