import { useEffect, useMemo, useRef } from "react";
import type { Dispatch, RefObject, SetStateAction } from "react";
import { motion } from "motion/react";
import { api } from "@/api/client";
import type { WorkItem } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { usePreferences } from "@/lib/preferences";
import { sourceDate } from "@/lib/format";
import { Icon } from "@/components/icon";
import { Button } from "@/components/ui/button";
import { ActionButton } from "@/components/action-button";
import { DataNotice, EmptyState, ErrorState, LoadingState, SeverityBadge, WorkflowBadge } from "@/components/states";

export interface WorkContext {
  query: string;
  selected: Set<string>;
  page: number;
  sort: "source-order" | "severity" | "title";
}
const pageSize = 50;
const severityRank = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };

export function WorkPage({ context, setContext, filterRef, openFinding }: {
  context: WorkContext;
  setContext: Dispatch<SetStateAction<WorkContext>>;
  filterRef: RefObject<HTMLInputElement | null>;
  openFinding: (finding: WorkItem, trigger: HTMLButtonElement) => void;
}) {
  const resource = useResource(api.work);
  const { reducedMotion } = usePreferences();
  const selectPage = useRef<HTMLInputElement>(null);
  const { query, selected, sort } = context;
  const matching = useMemo(() => {
    const term = query.trim().toLowerCase();
    const items = (resource.data?.items ?? []).filter((item) => `${item.title} ${item.assetName} ${item.ownerName ?? ""}`.toLowerCase().includes(term));
    if (sort === "severity") items.sort((a, b) => severityRank[a.severity] - severityRank[b.severity]);
    if (sort === "title") items.sort((a, b) => a.title.localeCompare(b.title));
    return items;
  }, [resource.data, query, sort]);
  const pageCount = Math.max(1, Math.ceil(matching.length / pageSize));
  const page = Math.min(context.page, pageCount - 1);
  const rows = matching.slice(page * pageSize, (page + 1) * pageSize);
  const selectedOnPage = rows.filter((item) => selected.has(item.id)).length;
  useEffect(() => {
    if (selectPage.current) selectPage.current.indeterminate = selectedOnPage > 0 && selectedOnPage < rows.length;
  }, [selectedOnPage, rows.length]);
  useEffect(() => {
    if (resource.error?.code === "forbidden") setContext((previous) => ({ ...previous, selected: new Set() }));
  }, [resource.error, setContext]);
  const updateQuery = (value: string) => setContext((previous) => ({ ...previous, query: value, page: 0 }));
  const select = (id: string, checked: boolean) => setContext((previous) => {
    const next = new Set(previous.selected);
    if (checked) next.add(id); else next.delete(id);
    return { ...previous, selected: next };
  });

  return <>
    <header className="page-heading">
      <div><p className="eyebrow">Your security work, in context</p><h1 tabIndex={-1} id="work-heading">Work</h1><p className="page-description">Understand the evidence. Keep the next step clear.</p></div>
      <div className="heading-actions">{resource.data && <DataNotice origin={resource.data.dataOrigin} />}<ActionButton variant="outline" onClick={resource.reload} disabled={resource.status === "loading"}><Icon name="refresh" />Refresh</ActionButton></div>
    </header>

    {resource.data && <div className="work-summary" aria-label="Current result summary">
      <div><span className="summary-label"><Icon name="layers" size={16} />Findings in view</span><strong>{matching.length.toLocaleString()}</strong><span>of {resource.data.total.toLocaleString()} returned by the API</span></div>
      <div><span className="summary-label"><Icon name="user" size={16} />Without an owner</span><strong>{matching.filter((item) => item.ownerName === null).length.toLocaleString()}</strong><span>ownership is not inferred</span></div>
      <div><span className="summary-label"><Icon name="clock" size={16} />Unknown scan time</span><strong>{matching.filter((item) => item.sourceScanAt === null).length.toLocaleString()}</strong><span>import time is kept separate</span></div>
    </div>}

    <section className="surface queue-surface" aria-label="Finding work queue">
      <div className="queue-toolbar">
        <div className="queue-title"><span className="section-mark" /><h2>Finding queue</h2><span className="subtle-pill">Read only</span></div>
        <div className="filter-field"><Icon name="search" size={17} /><input id="finding-filter" ref={filterRef} type="text" enterKeyHint="search" aria-label="Filter findings" placeholder="Filter by title, asset, or owner" value={query} onChange={(event) => updateQuery(event.target.value)} />{query && <button type="button" className="clear-filter" aria-label="Clear finding filter" onClick={() => { updateQuery(""); filterRef.current?.focus(); }}><Icon name="close" size={15} /></button>}</div>
      </div>
      {resource.error && <ErrorState error={resource.error} retry={resource.reload} stale={resource.data !== null} />}
      {resource.status === "loading" && !resource.data && <LoadingState label="Loading findings" />}
      {resource.status === "loading" && resource.data && <p className="inline-status" role="status"><Icon name="clock" size={15} />Refreshing findings. Current results stay in place.</p>}
      {resource.data && <>
        {selected.size > 0 && <motion.div role="status" className="selection-toolbar" initial={reducedMotion ? false : { opacity: 0, y: 3 }} animate={{ opacity: 1, y: 0 }}>
          <span className="selection-count">{selected.size}</span><span>selected<span className="selection-scope"> / {selectedOnPage} on this page</span></span>
          <span className="selection-boundary">Workflow changes are not available in this preview.</span>
          <Button variant="ghost" size="sm" onClick={() => setContext((previous) => ({ ...previous, selected: new Set() }))}>Clear selection</Button>
        </motion.div>}
        {resource.data.nextCursor !== null && <p className="inline-status"><Icon name="info" size={15} />More results exist on the service. Filtering and selection apply only to this loaded result set.</p>}
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
                <th>Owner</th><th>Workflow</th><th title="Original source scan time, not collection or import time">Source scan</th>
              </tr></thead>
              <tbody>{rows.map((item) => <tr key={item.id} className={selected.has(item.id) ? "is-selected" : undefined}>
                <td className="select-column"><input type="checkbox" aria-label={`Select ${item.title}`} checked={selected.has(item.id)} onChange={(event) => select(item.id, event.target.checked)} /></td>
                <td className="finding-column"><button type="button" className="finding-title" onClick={(event) => openFinding(item, event.currentTarget)}>{item.title}<Icon name="chevron" size={15} /></button><div className="asset-reference"><Icon name="code" size={13} /><span>{item.assetName}</span></div></td>
                <td><SeverityBadge severity={item.severity} /></td>
                <td><span className={`owner ${item.ownerName === null ? "unassigned" : ""}`}><span className="avatar">{item.ownerName ? item.ownerName.slice(0, 1).toUpperCase() : <Icon name="user" size={13} />}</span>{item.ownerName ?? "Unassigned"}</span></td>
                <td><WorkflowBadge value={item.workflowState} /></td>
                <td className="source-date">{item.sourceScanAt === null ? <span className="unknown-time"><Icon name="clock" size={14} />Unknown</span> : <time dateTime={item.sourceScanAt}>{sourceDate(item.sourceScanAt)}</time>}</td>
              </tr>)}</tbody>
            </table>
          </div>
          <footer className="table-footer"><span>{page * pageSize + 1}-{Math.min((page + 1) * pageSize, matching.length)} of {matching.length.toLocaleString()} loaded findings</span><div className="pagination"><Button variant="ghost" size="sm" disabled={page === 0} onClick={() => setContext((previous) => ({ ...previous, page: page - 1 }))}>Previous</Button><span>Page {page + 1} of {pageCount}</span><Button variant="ghost" size="sm" disabled={page + 1 >= pageCount} onClick={() => setContext((previous) => ({ ...previous, page: page + 1 }))}>Next</Button></div></footer>
        </>}
      </>}
    </section>
    <p className="view-footnote"><Icon name="shield" size={15} />Source evidence, human decisions, and verification are separate. Opening a finding triggers no external action.</p>
  </>;
}
