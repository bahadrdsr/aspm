import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { api, APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import type { Asset, AssetsResponse } from "@/api/types";
import { useResource } from "./use-resource";

export interface AssetPagination {
  visible: boolean;
  hasMore: boolean;
  pending: boolean;
  error: APIError | null;
  loadMore: () => void;
  retry: () => void;
}

export function useAssetPages() {
  const [read, setRead] = useState({ cursor: "", sequence: 0 });
  const rows = useRef(new Map<string, Asset>());
  const acknowledgements = useRef(new Map<string, { order: number; asset: Asset; pending: boolean }>());
  const acknowledgementOrder = useRef(0);
  const lastPage = useRef<AssetsResponse | null>(null);
  const frontier = useRef<{ cursor: string; next: string | null }>({ cursor: "", next: null });
  const pagingSeen = useRef(false);
  const scheduled = useRef(false);
  const mounted = useRef(true);
  const clear = useCallback(() => {
    rows.current.clear();
    acknowledgements.current.clear();
    lastPage.current = null;
    frontier.current = { cursor: "", next: null };
    pagingSeen.current = false;
  }, []);
  const applyAcknowledgements = useCallback(() => {
    // Only validated mutation receipts enter this cache; older reads remain fenced below.
    for (const acknowledged of acknowledgements.current.values()) {
      if (!acknowledged.pending) continue;
      rows.current.set(acknowledged.asset.id, acknowledged.asset);
      acknowledged.pending = false;
    }
  }, []);
  useLayoutEffect(() => {
    mounted.current = true;
    const signal = requestAuthority().signal;
    signal.addEventListener("abort", clear, { once: true });
    return () => { mounted.current = false; signal.removeEventListener("abort", clear); clear(); };
  }, [clear]);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
    const startedBefore = acknowledgementOrder.current;
    let page: AssetsResponse;
    try {
      page = await api.assets(scopedSignal, read.cursor === "" ? undefined : { cursor: read.cursor });
    } catch (cause: unknown) {
      scopedSignal.throwIfAborted();
      if (cause instanceof APIError) {
        if (["network", "unavailable"].includes(cause.code)) applyAcknowledgements();
        else clear();
      }
      throw cause;
    }
    scopedSignal.throwIfAborted();
    if (!mounted.current) throw new DOMException("Asset view closed", "AbortError");
    applyAcknowledgements();
    for (const asset of page.items) {
      const acknowledged = acknowledgements.current.get(asset.id);
      // Local ACK order fences an older read; it is not an asset revision or API field.
      rows.current.set(asset.id, acknowledged && acknowledged.order > startedBefore ? acknowledged.asset : asset);
    }
    lastPage.current = page;
    pagingSeen.current ||= page.nextCursor !== null;
    // Refreshing the first page does not rewind a continuation already reached.
    if (read.cursor >= frontier.current.cursor) frontier.current = { cursor: read.cursor, next: page.nextCursor };
    return page;
  }, [read, clear, applyAcknowledgements]);
  const resource = useResource(load);
  const pending = resource.status === "loading";
  function requestPage(cursor: string) {
    if (pending || scheduled.current) return;
    scheduled.current = true;
    setRead((previous) => ({ cursor, sequence: previous.sequence + 1 }));
  }
  function reload() { requestPage(""); }
  function retry() { requestPage(read.cursor); }
  function loadMore() {
    if (frontier.current.next !== null) requestPage(frontier.current.next);
  }
  function acknowledge(asset: Asset) {
    if (asset.workspaceId !== requestAuthority().workspace) throw new APIError("The asset acknowledgement belongs to a different workspace.", "invalid-response", false);
    const order = ++acknowledgementOrder.current;
    acknowledgements.current.set(asset.id, { order, asset, pending: true });
    applyAcknowledgements();
    // Preserve the existing post-save first-page refresh without dropping later rows.
    scheduled.current = true;
    setRead((previous) => ({ cursor: "", sequence: previous.sequence + 1 }));
  }
  const data = lastPage.current === null ? null : {
    ...lastPage.current, items: [...rows.current.values()], nextCursor: frontier.current.next,
  };
  const pagination: AssetPagination = { visible: pagingSeen.current, hasMore: frontier.current.next !== null,
    pending, error: resource.error, loadMore, retry };
  return { status: resource.status, error: resource.error, data, reload, retry, acknowledge,
    continuation: read.cursor !== "", pagination };
}
