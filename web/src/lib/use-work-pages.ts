import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import type { WorkItem, WorkResponse } from "@/api/types";
import { useResource } from "./use-resource";

export interface ConfirmedWorkUpdates {
  revision: number;
  items: ReadonlyMap<string, { revision: number; item: WorkItem }>;
}

export function useWorkPages(confirmed: ConfirmedWorkUpdates) {
  const [read, setRead] = useState({ cursor: "", sequence: 0 });
  const rows = useRef(new Map<string, { revision: number; item: WorkItem }>());
  const lastPage = useRef<WorkResponse | null>(null);
  const currentRevision = useRef(confirmed.revision);
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
    return () => { mounted.current = false; signal.removeEventListener("abort", clear); clear(); };
  }, [clear]);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    const authority = requestAuthority();
    const scopedSignal = AbortSignal.any([signal, authority.signal]);
    const revision = currentRevision.current;
    let page: WorkResponse;
    try {
      page = await api.work(scopedSignal, read.cursor === "" ? undefined : { cursor: read.cursor });
    } catch (cause: unknown) {
      scopedSignal.throwIfAborted();
      if (cause instanceof APIError && (cause.code === "forbidden" || cause.code === "not-found")) clear();
      throw cause;
    }
    scopedSignal.throwIfAborted();
    if (!mounted.current || requestAuthority().revision !== authority.revision) {
      throw new DOMException("Work view closed or workspace changed", "AbortError");
    }
    const merged = read.cursor === "" ? new Map<string, { revision: number; item: WorkItem }>() : new Map(rows.current);
    for (const item of page.items) merged.set(item.id, { revision, item });
    rows.current = merged;
    lastPage.current = page;
    return page;
  }, [read, clear]);
  const resource = useResource(load);
  const data = useMemo(() => {
    const page = lastPage.current;
    if (page === null) return null;
    return { ...page, items: [...rows.current.values()].map((row) => {
      const update = confirmed.items.get(row.item.id);
      // Only a read of this row can supersede its ACK fence, not an unrelated later page.
      return update && update.revision > row.revision ? update.item : row.item;
    }) };
  }, [resource.data, resource.status, resource.error, confirmed]);
  function requestPage(cursor: string) {
    if (resource.status === "loading" || scheduled.current) return;
    scheduled.current = true;
    setRead((previous) => ({ cursor, sequence: previous.sequence + 1 }));
  }
  function loadMore() {
    const next = lastPage.current?.nextCursor;
    if (next != null) requestPage(next);
  }
  return {
    data, error: resource.error, status: resource.status, pending: resource.status === "loading",
    continuation: read.cursor !== "", loadMore,
    reload: () => requestPage(""), retry: () => requestPage(read.cursor),
  };
}
