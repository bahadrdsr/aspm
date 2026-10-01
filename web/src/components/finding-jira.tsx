import { useLayoutEffect, useRef, useState } from "react";
import type { FindingDetail } from "@/api/types";
import type { JiraDeliveryResponse } from "@/api/jira-types";
import { invalidateJiraIntent, jiraFindingIdentity } from "@/api/jira-intents";
import { useSession } from "@/lib/session";
import { Button } from "./ui/button";
import { JiraDeliveryDetail } from "./jira-delivery-detail";
import { JiraDeliveryHistory } from "./jira-delivery-history";
import { JiraWorkItemReview } from "./jira-work-item-review";
import "./slack.css";
import "./jira.css";

export function FindingJira({ finding, current }: { finding: FindingDetail; current: boolean }) {
  const { workspace } = useSession();
  const section = useRef<HTMLElement>(null);
  const entry = useRef<HTMLButtonElement>(null);
  const [reviewOpen, setReviewOpen] = useState(false), [historyOpen, setHistoryOpen] = useState(false);
  const [selection, setSelection] = useState<{ id: string; initial: JiraDeliveryResponse | null; replay: boolean; sequence: number } | null>(null);
  const identity = jiraFindingIdentity(finding), previous = useRef(identity);
  useLayoutEffect(() => {
    if (previous.current !== identity) {
      const active = document.activeElement;
      const restoreEntry = active !== null && section.current?.contains(active) &&
        active.closest('[aria-label="Jira work item review"], [aria-label="Jira work item details"]') !== null;
      invalidateJiraIntent(finding.id); setReviewOpen(false); setSelection(null);
      previous.current = identity;
      if (restoreEntry) entry.current?.focus({ preventScroll: true });
    }
  }, [identity, finding.id]);
  function close() { setReviewOpen(false); entry.current?.focus({ preventScroll: true }); }
  function select(id: string, initial: JiraDeliveryResponse | null = null, replay = false) {
    setSelection((previous) => ({ id, initial, replay, sequence: (previous?.sequence ?? 0) + 1 }));
  }
  return <section ref={section} className="jira-work-items" aria-label="Finding Jira work items">
    <header className="jira-work-items-heading"><h3>Jira work items</h3>
      {workspace.role !== "viewer" && <Button ref={entry} type="button" variant="outline" size="sm" aria-expanded={reviewOpen}
        disabled={!current}
        onClick={() => setReviewOpen(true)}>Create Jira work item</Button>}
      <Button type="button" variant="ghost" size="sm" aria-expanded={historyOpen}
        onClick={() => setHistoryOpen((value) => !value)}>Jira work item history</Button></header>
    <p className="form-help">Explicit Jira review and manual history only. No finding changes or vendor permission are inferred.</p>
    {reviewOpen && workspace.role !== "viewer" && <JiraWorkItemReview finding={finding} current={current} onClose={close}
      onReceipt={(response, replay) => select(response.delivery.id, response, replay)} />}
    {selection && <JiraDeliveryDetail key={selection.sequence} id={selection.id} findingId={finding.id}
      initial={selection.initial} replay={selection.replay} />}
    {historyOpen && <JiraDeliveryHistory findingId={finding.id} onSelect={select} />}
  </section>;
}
