import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import { assessmentsApi } from "@/api/assessments";
import { reconcileAssessment } from "@/api/assessment-intents";
import type { Assessment, AssessmentSummary } from "@/api/assessment-types";
import { useResource } from "@/lib/use-resource";
import { ActionButton } from "../action-button";
import { FormError } from "../form-dialog";
import { Button } from "../ui/button";
import { AssessmentTime } from "./facts";

interface HistoryPage { items: AssessmentSummary[]; total: number; nextCursor: string | null }
function summary(value: Assessment): AssessmentSummary {
  const { id, state, dispatchState, attempts, createdAt } = value;
  return { id, state, dispatchState, attempts, createdAt };
}

export function useAssessmentHistory(findingId: string) {
  const [read, setRead] = useState<{ cursor: string | null; sequence: number }>({ cursor: null, sequence: 0 });
  const [, changed] = useState(0);
  const current = useRef<HistoryPage | null>(null);
  const receipts = useRef(new Map<string, { item: AssessmentSummary; order: number }>());
  const sequence = useRef(0), scheduled = useRef(false);
  const generation = useRef(0), withheld = useRef(true), mounted = useRef(false);
  const { workspace, revision, signal: authoritySignal } = requestAuthority();
  const clear = useCallback(() => {
    generation.current++;
    withheld.current = true;
    current.current = null;
    receipts.current.clear();
  }, []);
  useLayoutEffect(() => {
    mounted.current = true;
    authoritySignal.addEventListener("abort", clear, { once: true });
    return () => { mounted.current = false; authoritySignal.removeEventListener("abort", clear); clear(); };
  }, [clear, findingId, authoritySignal]);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    const scoped = AbortSignal.any([signal, authoritySignal]), started = ++sequence.current, startedGeneration = generation.current;
    try {
      const response = await assessmentsApi.history(findingId, read.cursor, scoped);
      scoped.throwIfAborted();
      if (!mounted.current || startedGeneration !== generation.current) throw new DOMException("Assessment history epoch ended", "AbortError");
      for (const item of response.items) reconcileAssessment(item);
      const previous = read.cursor === null ? [] : current.current?.items ?? [];
      const rows = new Map([...previous, ...response.items.map(summary)].map((item) => [item.id, item]));
      for (const [id, receipt] of receipts.current) {
        if (receipt.order > started) rows.set(id, receipt.item);
        else receipts.current.delete(id);
      }
      const page = { items: [...rows.values()], total: response.total, nextCursor: response.nextCursor };
      current.current = page;
      withheld.current = false;
      return page;
    } catch (cause) {
      if (!scoped.aborted && mounted.current && startedGeneration === generation.current &&
        (!(cause instanceof APIError) || !["network", "unavailable"].includes(cause.code))) clear();
      throw cause;
    }
  }, [findingId, read, clear, authoritySignal]);
  const resource = useResource(load);
  function requestPage(cursor: string | null) {
    if (resource.status === "loading" || scheduled.current) return;
    scheduled.current = true;
    setRead((prior) => ({ cursor, sequence: prior.sequence + 1 }));
  }
  // Consumers capture this closure before a write, not from the render that receives its ACK.
  const receiptGeneration = generation.current;
  const accept = useCallback((receipt: Assessment): boolean => {
    if (!mounted.current || authoritySignal.aborted || requestAuthority().revision !== revision ||
      receipt.workspaceId !== workspace || receipt.findingId !== findingId) return false;
    // Canonical identity can settle uncertainty without authorizing protected history.
    reconcileAssessment(receipt);
    if (receiptGeneration !== generation.current || withheld.current) return false;
    const item = summary(receipt), order = ++sequence.current;
    receipts.current.set(item.id, { item, order });
    if (current.current) {
      current.current = { ...current.current, items: [...new Map([...current.current.items, item].map((row) => [row.id, row])).values()] };
    }
    changed(order);
    return true;
  }, [receiptGeneration, findingId, workspace, revision, authoritySignal]);
  const page = withheld.current ? null : current.current;
  const rows = withheld.current ? [] : page?.items ?? [...receipts.current.values()].map((receipt) => receipt.item);
  return {
    page, rows, error: resource.error, pending: resource.status === "loading", accept,
    authorized: !withheld.current && (resource.status === "ready" || receipts.current.size > 0 && resource.error === null),
    refresh: () => requestPage(null), retry: () => requestPage(read.cursor),
    more: () => { if (page?.nextCursor) requestPage(page.nextCursor); },
  };
}

export function AssessmentHistory({ data, onSelect }: {
  data: ReturnType<typeof useAssessmentHistory>; onSelect: (id: string) => void;
}) {
  return <section className="surface assessment-section" aria-label="Assessment history">
    <header className="assessment-heading"><h3>Assessment history</h3>
      <ActionButton variant="outline" disabled={data.pending} onClick={data.refresh}>Refresh assessment history</ActionButton></header>
    <div className="assessment-content">
      <FormError error={data.error} />
      {data.error && <ActionButton variant="outline" disabled={data.pending} onClick={data.retry}>Retry assessment history</ActionButton>}
      {data.pending && <p role="status" className="form-help">Loading assessment history. Confirmed rows remain available unless access is denied.</p>}
      {data.page && <p className="form-help">{data.page.total} assessments at the last read; {data.rows.length} loaded.
        {" "}Pages and later receipts are not one atomic snapshot.</p>}
      {data.rows.length === 0 && data.authorized && !data.pending && !data.error && <div><h4>No assessments</h4>
        <p className="form-help">The authorized service response returned no assessments for this finding.</p></div>}
      {data.rows.length > 0 && <div className="assessment-table-scroll" role="region" aria-label="Assessment history results" tabIndex={0}>
        <table className="assessment-table" aria-label="Assessment history">
          <thead><tr><th scope="col">Assessment</th><th scope="col">Action</th></tr></thead>
          <tbody>{data.rows.map((item) => <tr key={item.id}>
            <td><code>{item.id}</code><p>{item.state}</p><p>Dispatch: {item.dispatchState}; attempts: {item.attempts}</p>
              <p><AssessmentTime value={item.createdAt} /></p></td>
            <td><Button type="button" variant="outline" size="sm" onClick={() => onSelect(item.id)}>View assessment</Button></td>
          </tr>)}</tbody>
        </table>
      </div>}
      {data.page?.nextCursor && <ActionButton variant="outline" disabled={data.pending} onClick={data.more}>Load more assessments</ActionButton>}
      <p className="form-help">Manual native ID pages of 100. History reads never queue, dispatch, poll or contact a provider.</p>
    </div>
  </section>;
}
