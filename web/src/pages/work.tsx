import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Dispatch, RefObject, SetStateAction } from "react";
import { motion } from "motion/react";
import type { WorkItem } from "@/api/types";
import { APIError, workSearchQuery } from "@/api/client";
import type { SavedWorkView } from "@/api/work-views";
import { useWorkPages } from "@/lib/use-work-pages";
import type { ConfirmedWorkUpdates } from "@/lib/use-work-pages";
import { usePreferences } from "@/lib/preferences";
import { sourceDate } from "@/lib/format";
import { Icon } from "@/components/icon";
import { Button } from "@/components/ui/button";
import { ActionButton } from "@/components/action-button";
import { FormError } from "@/components/form-dialog";
import { SavedWorkViews } from "@/components/saved-work-views";
import { BulkTriage } from "@/components/bulk-triage";
import { DataNotice, EmptyState, ErrorState, LoadingState, SeverityBadge, WorkflowBadge } from "@/components/states";

export type { ConfirmedWorkUpdates } from "@/lib/use-work-pages";

export interface WorkContext {
  query: string;
  confirmedQuery: string;
  selected: Set<string>;
  page: number;
  sort: "source-order" | "severity" | "title";
  changesOnly: boolean;
}
export function matchesWorkQuery(item: WorkItem, query: string): boolean {
  return `${item.title} ${item.assetName} ${item.ownerName ?? ""}`.toLowerCase().includes(query.trim().toLowerCase());
}
const pageSize = 50;
const severityRank = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };

export function WorkPage({ context, setContext, filterRef, openFinding, confirmed, membershipRevision, canWrite, onConfirmed }: {
  context: WorkContext;
  setContext: Dispatch<SetStateAction<WorkContext>>;
  filterRef: RefObject<HTMLInputElement | null>;
  openFinding: (finding: WorkItem, trigger: HTMLButtonElement) => void;
  confirmed: ConfirmedWorkUpdates;
  membershipRevision: number;
  canWrite: boolean;
  onConfirmed: (items: WorkItem[]) => void;
}) {
  const confirmQuery = useCallback((confirmedQuery: string) => setContext((previous) => previous.confirmedQuery === confirmedQuery
    ? previous : { ...previous, confirmedQuery, selected: new Set(), page: 0 }), [setContext]);
  const resource = useWorkPages(confirmed, context.confirmedQuery, confirmQuery, context.changesOnly);
  const applyRequest = useRef<AbortController | null>(null);
  const seenMembershipRevision = useRef(membershipRevision);
  const draftRevision = useRef(0);
  useLayoutEffect(() => () => { applyRequest.current?.abort(); }, []);
  useEffect(() => {
    if (membershipRevision <= seenMembershipRevision.current) return;
    seenMembershipRevision.current = membershipRevision;
    resource.reload();
  }, [membershipRevision, resource]);
  const data = resource.data;
  const [searchError, setSearchError] = useState<APIError | null>(null);
  const [bulkMessage, setBulkMessage] = useState<string | null>(null);
  const { reducedMotion } = usePreferences();
  const selectPage = useRef<HTMLInputElement>(null);
  const { query, selected, sort, changesOnly } = context;
  const matching = useMemo(() => {
    const items = (data?.items ?? []).filter((item) => matchesWorkQuery(item, query));
    if (sort === "severity") items.sort((a, b) => severityRank[a.severity] - severityRank[b.severity]);
    if (sort === "title") items.sort((a, b) => a.title.localeCompare(b.title));
    return items;
  }, [data, query, sort]);
  const pageCount = Math.max(1, Math.ceil(matching.length / pageSize));
  const page = Math.min(context.page, pageCount - 1);
  const rows = matching.slice(page * pageSize, (page + 1) * pageSize);
  const selectedOnPage = rows.filter((item) => selected.has(item.id)).length;
  useEffect(() => {
    if (selectPage.current) selectPage.current.indeterminate = selectedOnPage > 0 && selectedOnPage < rows.length;
  }, [selectedOnPage, rows.length]);
  useEffect(() => {
    if (resource.error?.code === "forbidden" || resource.error?.code === "not-found") {
      setContext((previous) => ({ ...previous, selected: new Set() }));
    }
  }, [resource.error, setContext]);
  const updateQuery = (value: string) => {
    draftRevision.current++;
    setSearchError(null);
    setContext((previous) => ({ ...previous, query: value, page: 0 }));
  };
  const cancelApply = () => {
    if (!applyRequest.current) return;
    applyRequest.current.abort();
    applyRequest.current = null;
    resource.suspend();
  };
  const beginApply = () => {
    cancelApply();
    resource.suspend();
    const controller = new AbortController(), startedDraft = draftRevision.current;
    applyRequest.current = controller;
    setSearchError(null);
    return {
      signal: controller.signal,
      submit: (view: SavedWorkView) => {
        if (controller.signal.aborted || applyRequest.current !== controller) return;
        const snapshot = { query: view.query, sort: view.sort };
        resource.search(snapshot.query, () => {
          if (controller.signal.aborted || applyRequest.current !== controller) return;
          applyRequest.current = null;
          setContext((previous) => ({
            ...previous, confirmedQuery: snapshot.query, sort: snapshot.sort, page: 0, selected: new Set(),
            query: draftRevision.current === startedDraft ? snapshot.query : previous.query,
          }));
        });
      },
    };
  };
  const clearSearch = () => {
    cancelApply();
    updateQuery("");
    resource.search("");
    filterRef.current?.focus();
  };
  const submitSearch = () => {
    cancelApply();
    try {
      const submitted = workSearchQuery(query);
      setSearchError(null);
      if (submitted === "") clearSearch();
      else resource.search(submitted);
    } catch (cause: unknown) {
      if (cause instanceof APIError) setSearchError(cause);
      else throw cause;
    }
  };
  const select = (id: string, checked: boolean) => setContext((previous) => {
    const next = new Set(previous.selected);
    if (checked) next.add(id); else next.delete(id);
    return { ...previous, selected: next };
  });

  return <>
    <header className="page-heading">
      <div><p className="eyebrow">Your security work, in context</p><h1 tabIndex={-1} id="work-heading">Work</h1><p className="page-description">Understand the evidence. Keep the next step clear.</p></div>
      <div className="heading-actions">{data && <DataNotice origin={data.dataOrigin} />}<ActionButton variant="outline" onClick={resource.reload} disabled={resource.status === "loading"}><Icon name="refresh" />Refresh</ActionButton></div>
    </header>

    <SavedWorkViews snapshot={{ query: context.confirmedQuery, sort }} beginApply={beginApply} cancelApply={cancelApply} />

    {data && <div className="work-summary" aria-label="Current result summary">
      <div><span className="summary-label"><Icon name="layers" size={16} />Findings in view</span><strong>{matching.length.toLocaleString()}</strong><span>of {data.total.toLocaleString()} returned by the API at the last read</span></div>
      <div><span className="summary-label"><Icon name="user" size={16} />Without an owner</span><strong>{matching.filter((item) => item.ownerName === null).length.toLocaleString()}</strong><span>ownership is not inferred</span></div>
      <div><span className="summary-label"><Icon name="clock" size={16} />Unknown scan time</span><strong>{matching.filter((item) => item.sourceScanAt === null).length.toLocaleString()}</strong><span>import time is kept separate</span></div>
    </div>}

    <section className="surface queue-surface" aria-label="Finding work queue">
      <div className="queue-toolbar">
        <div className="queue-title"><span className="section-mark" /><h2>Finding queue</h2><span className="subtle-pill">{canWrite ? "In-context actions" : "Read only"}</span></div>
        <div className="work-search-controls">
          <div className="filter-field"><Icon name="search" size={17} /><input id="finding-filter" ref={filterRef} type="text" enterKeyHint="search" aria-label="Filter findings"
            aria-invalid={searchError !== null || undefined} aria-describedby={searchError ? "work-search-error" : undefined}
            placeholder="Filter by title, asset, or owner" value={query} onChange={(event) => updateQuery(event.target.value)}
            onKeyDown={(event) => { if (event.key === "Enter" && !event.nativeEvent.isComposing) { event.preventDefault(); submitSearch(); } }} />
            {query && <button type="button" className="clear-filter" aria-label="Clear finding filter" onClick={() => { updateQuery(""); filterRef.current?.focus(); }}><Icon name="close" size={15} /></button>}</div>
          <ActionButton variant="outline" onClick={submitSearch}>Search all findings</ActionButton>
          <Button variant={changesOnly ? "default" : "outline"} aria-pressed={changesOnly}
            onClick={() => {
              const next = !changesOnly;
              setContext((previous) => ({ ...previous, changesOnly: next, page: 0 }));
              resource.changeMode(next);
            }}>Meaningful changes only</Button>
          <Button variant="ghost" onClick={clearSearch}>Clear search</Button>
        </div>
      </div>
      <p className="inline-status workspace-search-status" role="status" aria-label="Workspace search"><Icon name="search" size={15} /><span>
        {(data?.query ?? context.confirmedQuery) === "" ? "No server search. Results are scoped to this workspace." :
          <>Confirmed workspace server search: "{data?.query ?? context.confirmedQuery}". Server results for this workspace.</>}
        {data?.changeMode === "meaningful" && " Showing only new, changed, or reopened source findings."}
        {data?.membershipNeedsRefresh && " Canonical owner changes mean search membership needs refresh; loaded rows and reported counts retain the last read's membership."}
      </span></p>
      {searchError && <div id="work-search-error" className="inline-status"><FormError error={searchError} /></div>}
      {resource.error && (resource.continuation ? <div className="inline-status">
        <FormError error={resource.error} /><ActionButton variant="outline" disabled={resource.pending} onClick={resource.retry}>Retry more findings</ActionButton>
      </div> : resource.searching ? <div className="inline-status">
        <FormError error={resource.error} /><ActionButton variant="outline" disabled={resource.pending} onClick={resource.retry}>Retry search</ActionButton>
      </div> : <ErrorState error={resource.error} retry={resource.retry} stale={data !== null} />)}
      {resource.status === "loading" && !data && <LoadingState label="Loading findings" />}
      {data && <p className="inline-status" role={resource.status === "loading" ? "status" : undefined}><Icon name={resource.status === "loading" ? "clock" : "info"} size={15} />
        {resource.status === "loading" ? `${resource.continuation ? "Loading more findings." : resource.searching ? "Loading workspace search." : "Refreshing findings."} Last confirmed results stay in place.` :
          resource.error ? "Read failed. Showing the last confirmed findings." : "Loaded findings. Refresh to check for updates."}</p>}
      {data && <>
        {selected.size > 0 && <motion.div role="status" className="selection-toolbar" initial={reducedMotion ? false : { opacity: 0, y: 3 }} animate={{ opacity: 1, y: 0 }}>
          <span className="selection-count">{selected.size}</span><span>selected<span className="selection-scope"> / {selectedOnPage} on this page</span></span>
          {canWrite ? <BulkTriage findingIds={[...selected]} onConfirmed={onConfirmed}
            onApplied={(next) => {
              setBulkMessage(next);
              setContext((previous) => ({ ...previous, selected: new Set() }));
            }} /> :
            <span className="selection-boundary">Read-only selection. Open a finding to review its evidence.</span>}
          <Button variant="ghost" size="sm" onClick={() => setContext((previous) => ({ ...previous, selected: new Set() }))}>Clear selection</Button>
        </motion.div>}
        {bulkMessage && <p className="inline-status" role="status">{bulkMessage}</p>}
        <p className="inline-status"><Icon name="info" size={15} />Filtering and Finding/Severity sorting apply only to loaded findings.</p>
        <p className="inline-status" role="status" aria-label="Finding pagination"><Icon name="info" size={15} />
          {data.items.length.toLocaleString()} loaded findings; {data.total.toLocaleString()} total reported by the last returned page.{" "}
          {data.nextCursor !== null ? "More results remain on the service. " : "The last returned page has no continuation. "}
          Pages and counts are not a single atomic snapshot.</p>
        {matching.length === 0 ? <EmptyState title="No findings" description={query ? "Nothing in the loaded results matches this filter. Try a different title, asset, or owner." : "The service returned no findings in this view. Connect a source when verified integrations become available."}>
          {query ? <Button variant="outline" onClick={() => updateQuery("")}>Clear filter</Button> : <Button asChild variant="outline"><a href="#/integrations">Explore integrations<Icon name="arrow" /></a></Button>}
        </EmptyState> : <>
          <div className="table-scroll" tabIndex={0} role="region" aria-label="Finding results">
            <table aria-label="Findings" className="finding-table">
              <thead><tr>
                <th className="select-column"><input type="checkbox" ref={selectPage} aria-label="Select all findings on this page" checked={rows.length > 0 && selectedOnPage === rows.length} onChange={(event) => {
                  const checked = event.target.checked;
                  setContext((previous) => {
                    const next = new Set(previous.selected);
                    for (const item of rows) { if (checked) next.add(item.id); else next.delete(item.id); }
                    return { ...previous, selected: next };
                  });
                }} /></th>
                <th className="finding-column" aria-sort={sort === "title" ? "ascending" : "none"}><button type="button" onClick={() => setContext((previous) => ({ ...previous, sort: previous.sort === "title" ? "source-order" : "title", page: 0 }))}>Finding<Icon name="filter" size={13} /></button></th>
                <th aria-sort={sort === "severity" ? "ascending" : "none"}><button type="button" onClick={() => setContext((previous) => ({ ...previous, sort: previous.sort === "severity" ? "source-order" : "severity", page: 0 }))}>Severity<Icon name="filter" size={13} /></button></th>
                <th>Owner</th><th>Workflow</th><th>Source change</th>
                <th title="Original source scan time, not collection or import time">Source scan</th>
              </tr></thead>
              <tbody>{rows.map((item) => <tr key={item.id} className={selected.has(item.id) ? "is-selected" : undefined}>
                <td className="select-column"><input type="checkbox" aria-label={`Select ${item.title}`} checked={selected.has(item.id)} onChange={(event) => select(item.id, event.target.checked)} /></td>
                <td className="finding-column"><button type="button" className="finding-title" onClick={(event) => openFinding(item, event.currentTarget)}>{item.title}<Icon name="chevron" size={15} /></button><div className="asset-reference"><Icon name="code" size={13} /><span>{item.assetName}</span></div></td>
                <td><SeverityBadge severity={item.severity} /></td>
                <td><span className={`owner ${item.ownerName === null ? "unassigned" : ""}`}><span className="avatar">{item.ownerName ? item.ownerName.slice(0, 1).toUpperCase() : <Icon name="user" size={13} />}</span>{item.ownerName ?? "Unassigned"}</span></td>
                <td><WorkflowBadge value={item.workflowState} /></td>
                <td><span className={`subtle-pill change-${item.changeKind}`}>{item.changeKind}</span></td>
                <td className="source-date">{item.sourceScanAt === null ? <span className="unknown-time"><Icon name="clock" size={14} />Unknown</span> : <time dateTime={item.sourceScanAt}>{sourceDate(item.sourceScanAt)}</time>}</td>
              </tr>)}</tbody>
            </table>
          </div>
          <footer className="table-footer"><span>{page * pageSize + 1}-{Math.min((page + 1) * pageSize, matching.length)} of {matching.length.toLocaleString()} loaded findings</span><div className="pagination"><Button variant="ghost" size="sm" disabled={page === 0} onClick={() => setContext((previous) => ({ ...previous, page: page - 1 }))}>Previous</Button><span>Page {page + 1} of {pageCount}</span><Button variant="ghost" size="sm" disabled={page + 1 >= pageCount} onClick={() => setContext((previous) => ({ ...previous, page: page + 1 }))}>Next</Button></div></footer>
        </>}
        {data.nextCursor !== null && <div className="inline-status"><ActionButton variant="outline" disabled={resource.pending}
          onClick={resource.loadMore}>Load more findings<Icon name="chevron" /></ActionButton></div>}
      </>}
    </section>
    <p className="view-footnote"><Icon name="shield" size={15} />Source evidence, human decisions, and verification are separate. Opening a finding triggers no external action.</p>
  </>;
}
