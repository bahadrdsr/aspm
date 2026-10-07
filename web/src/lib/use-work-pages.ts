import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import type { WorkItem, WorkResponse } from "@/api/types";
import { useResource } from "./use-resource";

export interface ConfirmedWorkUpdates {
  revision: number;
  items: ReadonlyMap<string, { revision: number; item: WorkItem }>;
}

export function useWorkPages(confirmed: ConfirmedWorkUpdates, query: string,
  onQueryConfirmed: (query: string) => void, meaningfulChanges: boolean) {
  const [read, setRead] = useState<{
    query: string; cursor: string; sequence: number; search: boolean; suspended: boolean;
    meaningful: boolean; complete?: () => void;
  }>({ query, cursor: "", sequence: 0, search: false, suspended: false, meaningful: meaningfulChanges });
  const rows = useRef(new Map<string, { revision: number; item: WorkItem }>());
  const lastPage = useRef<(WorkResponse & { query: string }) | null>(null);
  const currentRevision = useRef(confirmed.revision);
  const sequence = useRef(0);
  const activeRequest = useRef<AbortController | null>(null);
  const scheduled = useRef(false);
  const mounted = useRef(true);
  const clear = useCallback(() => {
    rows.current.clear();
    lastPage.current = null;
  }, []);
  useLayoutEffect(() => { currentRevision.current = confirmed.revision; }, [confirmed.revision]);
  useLayoutEffect(() => {
    mounted.current = true;
    const signal = requestAuthority().signal;
    signal.addEventListener("abort", clear, { once: true });
    return () => { mounted.current = false; activeRequest.current?.abort(); signal.removeEventListener("abort", clear); clear(); };
  }, [clear]);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    if (read.suspended) return null;
    const authority = requestAuthority();
    const controller = new AbortController();
    activeRequest.current = controller;
    const scopedSignal = AbortSignal.any([signal, authority.signal, controller.signal]);
    const revision = currentRevision.current;
    let page: WorkResponse;
    try {
      page = await api.work(scopedSignal, read.cursor === ""
        ? { q: read.query, meaningfulChanges: read.meaningful }
        : { q: read.query, cursor: read.cursor, meaningfulChanges: read.meaningful });
    } catch (cause: unknown) {
      scopedSignal.throwIfAborted();
      if (read.sequence !== sequence.current) throw new DOMException("Work read superseded", "AbortError");
      if (cause instanceof APIError && (cause.code === "forbidden" || cause.code === "not-found")) clear();
      throw cause;
    }
    scopedSignal.throwIfAborted();
    if (!mounted.current || read.sequence !== sequence.current || requestAuthority().revision !== authority.revision) {
      throw new DOMException("Work view closed, superseded or workspace changed", "AbortError");
    }
    if (page.changeMode !== (read.meaningful ? "meaningful" : "all")) {
      throw new APIError("The service returned a different Work change mode.", "invalid-response", true);
    }
    const merged = read.cursor === "" ? new Map<string, { revision: number; item: WorkItem }>() : new Map(rows.current);
    for (const item of page.items) merged.set(item.id, { revision, item });
    rows.current = merged;
    lastPage.current = { ...page, query: read.query };
    onQueryConfirmed(read.query);
    read.complete?.();
    return page;
  }, [read, clear, onQueryConfirmed]);
  const resource = useResource(load);
  const data = useMemo(() => {
    const page = lastPage.current;
    if (page === null) return null;
    let membershipNeedsRefresh = false;
    const items = [...rows.current.values()].map((row) => {
      const update = confirmed.items.get(row.item.id);
      // Only a read of this row can supersede its ACK fence, not an unrelated later page.
      if (page.query !== "" && update && update.revision > row.revision && update.item.ownerName !== row.item.ownerName) {
        membershipNeedsRefresh = true;
      }
      return update && update.revision > row.revision ? update.item : row.item;
    });
    return { ...page, items, membershipNeedsRefresh };
  }, [resource.data, resource.status, resource.error, confirmed]);
  function requestPage(query: string, cursor: string, search = false, complete?: () => void,
    meaningful = read.meaningful) {
    if (((resource.status === "loading" && !read.suspended) || scheduled.current) &&
      (!search || (!complete && !read.complete && query === read.query && cursor === read.cursor))) return;
    scheduled.current = true;
    activeRequest.current?.abort();
    sequence.current += 1;
    setRead({ query, cursor, search, sequence: sequence.current, suspended: false, meaningful, complete });
  }
  function suspend() {
    if (resource.status !== "loading" && !scheduled.current) return;
    activeRequest.current?.abort();
    scheduled.current = false;
    sequence.current += 1;
    setRead((previous) => ({ ...previous, sequence: sequence.current, suspended: true, complete: undefined }));
  }
  function loadMore() {
    const page = lastPage.current;
    if (page?.nextCursor != null) requestPage(page.query, page.nextCursor);
  }
  return {
    data, error: read.suspended ? null : resource.error, status: read.suspended ? "ready" : resource.status,
    pending: !read.suspended && resource.status === "loading",
    continuation: read.cursor !== "", searching: read.search || read.query !== "", loadMore, suspend,
    search: (query: string, complete?: () => void) => requestPage(query, "", true, complete),
    changeMode: (meaningful: boolean) => requestPage(lastPage.current?.query ?? query, "", false, undefined, meaningful),
    reload: () => requestPage(lastPage.current?.query ?? query, ""),
    retry: () => requestPage(read.query, read.cursor, read.search, read.complete),
  };
}
