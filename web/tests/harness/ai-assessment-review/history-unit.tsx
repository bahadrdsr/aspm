import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { api } from "../../../src/api/client";
import { setRequestWorkspace } from "../../../src/api/authorization";
import { assessmentsApi } from "../../../src/api/assessments";
import { beginAssessment, pendingAssessment } from "../../../src/api/assessment-intents";
import type { Assessment } from "../../../src/api/assessment-types";
import { useScopedAction } from "../../../src/lib/use-scoped-action";
import { Preferences } from "../../../src/lib/preferences";
import { AssessmentHistory, useAssessmentHistory } from "../../../src/components/finding-assessments/history";
import { unitCancelId, unitFindingId, unitPreviewId, unitWorkspaceId } from "./inputs";

type Identity = Pick<Assessment, "id" | "previewId" | "idempotencyKey">;

function HistoryUnit({ workspaceId, requestedBy }: { workspaceId: string; requestedBy: string }) {
  const history = useAssessmentHistory(unitFindingId);
  const action = useScopedAction();
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [selected, setSelected] = useState("");
  const unresolved = pendingAssessment(workspaceId, requestedBy, unitFindingId);
  function accept(receipt: Assessment) {
    history.accept(receipt);
    setIdentity({ id: receipt.id, previewId: receipt.previewId, idempotencyKey: receipt.idempotencyKey });
  }
  function queue() {
    void action.run((signal) => {
      const intent = beginAssessment(workspaceId, requestedBy, unitFindingId, unitPreviewId);
      return assessmentsApi.queue(unitFindingId, requestedBy, { previewId: intent.previewId,
        idempotencyKey: intent.idempotencyKey, consent: true }, signal);
    }, accept);
  }
  function cancel() {
    void action.run(async (signal) => {
      const original = await assessmentsApi.detail(unitCancelId, unitFindingId, signal);
      return assessmentsApi.cancel(original, signal);
    }, accept);
  }
  return <main>
    <h1>Production-hook invariant only, not the application</h1>
    <p>The parent pane is not mounted. This unit does not prove its browser race.</p>
    <AssessmentHistory data={history} onSelect={setSelected} />
    <button type="button" disabled={action.pending || !history.authorized || history.pending} onClick={queue}>Unit start queue</button>
    <button type="button" disabled={action.pending || !history.authorized || history.pending} onClick={cancel}>Unit start cancellation</button>
    <output aria-label="Unit history authorized">{String(history.authorized)}</output>
    <output aria-label="Unit write pending">{String(action.pending)}</output>
    <output aria-label="Unit receipt identity">{JSON.stringify(identity)}</output>
    <output aria-label="Unit unresolved identity">{JSON.stringify(unresolved)}</output>
    <output aria-label="Unit selected identity">{selected}</output>
    {action.error && <p role="alert">{action.error.message}</p>}
  </main>;
}

const session = await api.session(new AbortController().signal);
const workspace = session.workspaces.find((value) => value.id === unitWorkspaceId);
if (!workspace) throw new Error("The unit needs its verified synthetic workspace.");
setRequestWorkspace(workspace.id);
createRoot(document.getElementById("history-unit-root")!).render(
  <StrictMode><Preferences><HistoryUnit workspaceId={workspace.id} requestedBy={session.user.id} /></Preferences></StrictMode>,
);
