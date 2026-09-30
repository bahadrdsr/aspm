import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import { isWorkViewDenied, workViewsApi } from "@/api/work-views";
import type { SavedWorkView } from "@/api/work-views";
import { useResource } from "./use-resource";

interface ViewOperation { epoch: number; order: number; authority: number; id?: string }
interface ListMetadata { ids: string[]; total: number; nextCursor: string | null }

export function useSavedWorkViews() {
  const [, changed] = useState(0);
  const [epoch, setEpoch] = useState(0);
  const [read, setRead] = useState<{ cursor: string | null; sequence: number }>({ cursor: null, sequence: 0 });
  const state = useRef({
    active: true, epoch: 0, sequence: 0, page: null as ListMetadata | null,
    metadata: new Map<string, SavedWorkView>(), revisions: new Map<string, string>(),
    withheld: new Map<string, number>(), receipts: new Map<string, number>(),
  });
  const scheduled = useRef(false);
  const notify = useCallback(() => changed((previous) => previous + 1), []);
  const begin = useCallback((id?: string): ViewOperation => {
    const current = state.current;
    if (!current.active) throw new DOMException("Saved views closed", "AbortError");
    return { epoch: current.epoch, order: ++current.sequence, authority: requestAuthority().revision, id };
  }, []);
  const current = useCallback((operation: ViewOperation, signal: AbortSignal) => {
    signal.throwIfAborted();
    const current = state.current;
    if (!current.active || operation.authority !== requestAuthority().revision || operation.epoch !== current.epoch) {
      throw new DOMException("Saved view context ended", "AbortError");
    }
    if (operation.id && (current.withheld.get(operation.id) ?? 0) > operation.order) {
      throw new APIError("Saved view metadata was withheld after this request began. Read its current authorized detail again.", "conflict", false);
    }
  }, []);
  const deny = useCallback((id?: string) => {
    const current = state.current;
    if (id !== undefined) {
      current.withheld.set(id, ++current.sequence);
      current.metadata.delete(id);
      current.receipts.delete(id);
    } else {
      current.epoch++;
      current.page = null;
      current.metadata.clear();
      current.receipts.clear();
      current.withheld.clear();
      setEpoch(current.epoch);
    }
    notify();
  }, [notify]);
  useLayoutEffect(() => {
    state.current.active = true;
    const signal = requestAuthority().signal;
    const clear = () => {
      const current = state.current;
      current.active = false;
      current.epoch++;
      current.page = null;
      current.metadata.clear();
      current.revisions.clear();
      current.receipts.clear();
      current.withheld.clear();
    };
    signal.addEventListener("abort", clear, { once: true });
    return () => { signal.removeEventListener("abort", clear); clear(); };
  }, []);
  const validate = useCallback((view: SavedWorkView, operation: ViewOperation) => {
    const current = state.current;
    if ((current.withheld.get(view.id) ?? 0) > operation.order) {
      throw new APIError("This saved view was withheld during the read. Refresh authorized metadata again.", "conflict", false);
    }
    const known = current.revisions.get(view.id);
    if (known && BigInt(view.revision) < BigInt(known)) {
      throw new APIError("The saved view revision is older than already observed metadata. Retry its current authorized read.", "invalid-response", false);
    }
    const previous = current.metadata.get(view.id);
    if (previous && (previous.createdAt !== view.createdAt || BigInt(view.revision) === BigInt(previous.revision) &&
      JSON.stringify(view) !== JSON.stringify(previous))) {
      throw new APIError("The service returned inconsistent saved view revision metadata. Retry its current authorized read.", "invalid-response", false);
    }
  }, []);
  const observe = useCallback((view: SavedWorkView) => {
    const current = state.current;
    current.metadata.set(view.id, view);
    current.revisions.set(view.id, view.revision);
    current.withheld.delete(view.id);
  }, []);
  const readView = useCallback(async (id: string, signal: AbortSignal): Promise<SavedWorkView> => {
    const operation = begin(id);
    const scoped = AbortSignal.any([signal, requestAuthority().signal]);
    let view: SavedWorkView;
    try { view = await workViewsApi.detail(id, scoped); }
    catch (cause) {
      current(operation, scoped);
      if (isWorkViewDenied(cause)) deny(id);
      throw cause;
    }
    current(operation, scoped);
    validate(view, operation);
    observe(view);
    notify();
    return view;
  }, [begin, current, deny, validate, observe, notify]);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    const operation = begin();
    const scoped = AbortSignal.any([signal, requestAuthority().signal]);
    let response;
    try { response = await workViewsApi.list(read.cursor, scoped); }
    catch (cause) {
      current(operation, scoped);
      if (isWorkViewDenied(cause)) deny();
      throw cause;
    }
    current(operation, scoped);
    for (const view of response.items) validate(view, operation);
    const previous = read.cursor === null ? [] : state.current.page?.ids;
    if (previous === undefined) throw new APIError("The saved view continuation no longer has an authorized first page. Refresh saved views.", "conflict", false);
    if (response.items.some((view) => previous.includes(view.id))) {
      throw new APIError("The service repeated a saved view identifier across native pages. No page was appended.", "invalid-response", false);
    }
    for (const view of response.items) observe(view);
    if (read.cursor === null) {
      for (const [id, order] of state.current.receipts) if (order <= operation.order) state.current.receipts.delete(id);
    }
    state.current.page = {
      ids: [...previous, ...response.items.map((view) => view.id)], total: response.total, nextCursor: response.nextCursor,
    };
    return state.current.page;
  }, [begin, current, deny, validate, observe, read]);
  const resource = useResource(load);
  function requestPage(cursor: string | null) {
    if (resource.status === "loading" || scheduled.current) return;
    scheduled.current = true;
    setRead((previous) => ({ cursor, sequence: previous.sequence + 1 }));
  }
  const accept = useCallback((operation: ViewOperation, view: SavedWorkView, signal: AbortSignal) => {
    current(operation, signal);
    validate(view, operation);
    observe(view);
    state.current.receipts.set(view.id, ++state.current.sequence);
    notify();
  }, [current, validate, observe, notify]);
  const acceptDelete = useCallback((operation: ViewOperation, view: SavedWorkView, signal: AbortSignal) => {
    current(operation, signal);
    state.current.metadata.delete(view.id);
    state.current.receipts.delete(view.id);
    state.current.withheld.set(view.id, ++state.current.sequence);
    notify();
  }, [current, notify]);
  const failedWrite = useCallback((operation: ViewOperation, error: APIError) => {
    if (state.current.active && operation.epoch === state.current.epoch &&
      operation.authority === requestAuthority().revision && isWorkViewDenied(error)) deny(operation.id);
  }, [deny]);
  const page = state.current.page;
  const ids = new Set([...(page?.ids ?? []), ...state.current.receipts.keys()]);
  const rows = [...ids].sort().flatMap((id) => {
    const view = state.current.metadata.get(id);
    return view ? [view] : [];
  });
  return {
    rows, page, epoch, pending: resource.status === "loading", error: resource.error,
    refresh: () => requestPage(null), retry: () => requestPage(read.cursor),
    more: () => { if (page?.nextCursor) requestPage(page.nextCursor); },
    readView, beginWrite: begin, accept, acceptDelete, failedWrite,
    get: (id: string) => state.current.metadata.get(id) ?? null,
  };
}

export type SavedWorkViewsData = ReturnType<typeof useSavedWorkViews>;
