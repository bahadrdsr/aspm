import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import type { SourcePage } from "@/api/source-types";
import { useResource } from "@/lib/use-resource";

export function useSourcePage<T extends { id: string }>(reader: (cursor: string | null, signal: AbortSignal) => Promise<SourcePage<T>>) {
  const [read, setRead] = useState<{ cursor: string | null; sequence: number }>({ cursor: null, sequence: 0 });
  const [, renderAcknowledgement] = useState(0);
  const current = useRef<SourcePage<T> | null>(null);
  const acknowledgements = useRef(new Map<string, { item: T; order: number }>());
  const order = useRef(0), scheduled = useRef(false), mounted = useRef(true);
  const clear = useCallback(() => { current.current = null; acknowledgements.current.clear(); }, []);
  useLayoutEffect(() => {
    mounted.current = true;
    const signal = requestAuthority().signal;
    signal.addEventListener("abort", clear, { once: true });
    return () => { mounted.current = false; signal.removeEventListener("abort", clear); clear(); };
  }, [clear]);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
    const started = order.current;
    let response: SourcePage<T>;
    try { response = await reader(read.cursor, scopedSignal); }
    catch (cause: unknown) {
      scopedSignal.throwIfAborted();
      if (cause instanceof APIError && !["network", "unavailable"].includes(cause.code)) clear();
      throw cause;
    }
    scopedSignal.throwIfAborted();
    if (!mounted.current) throw new DOMException("Source view closed", "AbortError");
    const items = new Map(response.items.map((item) => [item.id, item]));
    for (const value of acknowledgements.current.values()) if (value.order > started) items.set(value.item.id, value.item);
    current.current = { ...response, items: [...items.values()] };
    return response;
  }, [reader, read, clear]);
  const resource = useResource(load);
  function requestPage(cursor: string | null) {
    if (resource.status === "loading" || scheduled.current) return;
    scheduled.current = true;
    setRead((previous) => ({ cursor, sequence: previous.sequence + 1 }));
  }
  function accept(item: T) {
    const revision = ++order.current;
    acknowledgements.current.set(item.id, { item, order: revision });
    const page = current.current;
    if (page) {
      const items = new Map(page.items.map((value) => [value.id, value]));
      items.set(item.id, item);
      current.current = { ...page, items: [...items.values()] };
    }
    renderAcknowledgement(revision);
  }
  return {
    data: current.current, pending: resource.status === "loading", error: resource.error, accept,
    refresh: () => requestPage(null), retry: () => requestPage(read.cursor),
    more: () => { if (current.current?.nextCursor) requestPage(current.current.nextCursor); },
  };
}
