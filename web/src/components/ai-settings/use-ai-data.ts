import { useCallback, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { APIError } from "@/api/client";
import { aiApi } from "@/api/ai";
import type { AIGrant, AIPage, AIPolicy, AIProfile } from "@/api/ai-types";
import { requestAuthority } from "@/api/authorization";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";

export function sameMetadata<T>(left: T | null, right: T | null): boolean {
  return left === right || left !== null && right !== null && JSON.stringify(left) === JSON.stringify(right);
}

function transient(cause: unknown): boolean {
  return cause instanceof APIError && (cause.code === "network" || cause.code === "unavailable");
}

export class AIMetadata<T> {
  private sequence = 0;
  private barrier = 0;
  private version = 0;
  private active = true;
  private entries = new Map<string, { value: T | null; order: number }>();
  private receipts = new Set<string>();
  private listeners = new Set<() => void>();
  constructor(private identify: (item: T) => string) {}
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  snapshot = () => this.version;
  private changed() { this.version++; for (const listener of this.listeners) listener(); }
  get = (id: string): T | null => this.entries.get(id)?.value ?? null;
  values = (): T[] => [...this.entries.values()].flatMap((item) => item.value === null ? [] : [item.value]);
  receiptValues = (): T[] => [...this.receipts].flatMap((id) => { const item = this.get(id); return item === null ? [] : [item]; });
  resume = () => { this.active = true; };
  clear = () => {
    this.barrier = ++this.sequence;
    this.entries.clear();
    this.receipts.clear();
    this.changed();
  };
  close = () => { this.active = false; this.clear(); };
  deny = (id?: string) => {
    if (id === undefined) this.clear();
    else {
      this.entries.set(id, { value: null, order: ++this.sequence });
      this.receipts.delete(id);
      this.changed();
    }
  };
  startWrite = () => ++this.sequence;
  accept = (item: T, started: number) => {
    const id = this.identify(item);
    const previous = this.entries.get(id);
    if (!this.active || started <= this.barrier || previous?.value === null && previous.order > started) {
      throw new APIError("The write receipt arrived after metadata was withheld. Its stored outcome cannot be displayed. Refresh an authorized read before another change.", "forbidden", false);
    }
    this.entries.set(id, { value: item, order: ++this.sequence });
    this.receipts.add(id);
    this.changed();
  };
  private current(order: number, signal: AbortSignal) {
    signal.throwIfAborted();
    if (!this.active) throw new DOMException("AI settings closed", "AbortError");
    if (order <= this.barrier) throw new APIError("AI metadata was withheld after this read began. Review current configuration again.", "conflict", false);
  }
  private receive(item: T, order: number) {
    // Order requests and receipts locally; server revisions are opaque identities.
    const id = this.identify(item), previous = this.entries.get(id);
    if (!previous || previous.order <= order) this.entries.set(id, { value: item, order });
  }
  read = async (id: string, operation: (signal: AbortSignal) => Promise<T>, signal: AbortSignal): Promise<T> => {
    const order = ++this.sequence;
    const scoped = AbortSignal.any([signal, requestAuthority().signal]);
    let value: T;
    try { value = await operation(scoped); }
    catch (cause) {
      if (!scoped.aborted && this.active && !transient(cause)) this.deny(id);
      throw cause;
    }
    this.current(order, scoped);
    this.receive(value, order);
    this.changed();
    const canonical = this.get(id);
    if (!sameMetadata(canonical, value)) throw new APIError("Configuration changed during this read. Review current configuration again.", "conflict", false);
    return value;
  };
  readPage = async <U extends T & { id: string }>(
    operation: (signal: AbortSignal) => Promise<AIPage<U>>, signal: AbortSignal,
  ): Promise<AIPage<U>> => {
    const order = ++this.sequence;
    const scoped = AbortSignal.any([signal, requestAuthority().signal]);
    let response: AIPage<U>;
    try { response = await operation(scoped); }
    catch (cause) {
      if (!scoped.aborted && this.active && !transient(cause)) this.clear();
      throw cause;
    }
    this.current(order, scoped);
    for (const item of response.items) this.receive(item, order);
    this.changed();
    return response;
  };
}

function useMetadata<T>(identify: (item: T) => string): AIMetadata<T> {
  const [metadata] = useState(() => new AIMetadata(identify));
  useSyncExternalStore(metadata.subscribe, metadata.snapshot, metadata.snapshot);
  useLayoutEffect(() => {
    metadata.resume();
    const signal = requestAuthority().signal;
    signal.addEventListener("abort", metadata.close, { once: true });
    return () => { signal.removeEventListener("abort", metadata.close); metadata.close(); };
  }, [metadata]);
  return metadata;
}

function useAIPage<T extends { id: string }>(
  reader: (cursor: string | null, signal: AbortSignal) => Promise<AIPage<T>>, metadata: AIMetadata<T>,
) {
  const [read, setRead] = useState<{ cursor: string | null; sequence: number }>({ cursor: null, sequence: 0 });
  const current = useRef<AIPage<T> | null>(null);
  const scheduled = useRef(false);
  useLayoutEffect(() => () => { current.current = null; }, []);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    try {
      const response = await reader(read.cursor, signal);
      signal.throwIfAborted();
      const prior = read.cursor === null ? [] : current.current?.items ?? [];
      current.current = { ...response, items: [...new Map([...prior, ...response.items].map((item) => [item.id, item])).values()] };
      return response;
    } catch (cause) {
      if (!signal.aborted && !transient(cause)) current.current = null;
      throw cause;
    }
  }, [reader, read]);
  const resource = useResource(load);
  function requestPage(cursor: string | null) {
    if (resource.status === "loading" || scheduled.current) return;
    scheduled.current = true;
    setRead((previous) => ({ cursor, sequence: previous.sequence + 1 }));
  }
  const page = current.current;
  const rows = [...new Map([...(page?.items ?? []), ...metadata.receiptValues()].flatMap((item) => {
    const canonical = metadata.get(item.id);
    return canonical === null ? [] : [[item.id, canonical] as const];
  })).values()];
  return {
    page, rows, pending: resource.status === "loading", error: resource.error,
    refresh: () => requestPage(null), retry: () => requestPage(read.cursor),
    more: () => { if (page?.nextCursor) requestPage(page.nextCursor); },
  };
}

export function useAIData(workspaceId: string) {
  const profiles = useMetadata<AIProfile>((item) => item.id);
  const policies = useMetadata<AIPolicy>((item) => item.workspaceId);
  const grants = useMetadata<AIGrant>((item) => item.id);
  const readProfile = useCallback((id: string, signal: AbortSignal) =>
    profiles.read(id, (scoped) => aiApi.profile(id, scoped), signal), [profiles]);
  const readPolicy = useCallback((signal: AbortSignal) =>
    policies.read(workspaceId, aiApi.policy, signal), [policies, workspaceId]);
  const readGrant = useCallback((id: string, signal: AbortSignal) =>
    grants.read(id, (scoped) => aiApi.grant(id, scoped), signal), [grants]);
  const readProfiles = useCallback((cursor: string | null, signal: AbortSignal) =>
    profiles.readPage((scoped) => aiApi.profiles(cursor, scoped), signal), [profiles]);
  const readGrants = useCallback((cursor: string | null, signal: AbortSignal) =>
    grants.readPage((scoped) => aiApi.grants(cursor, scoped), signal), [grants]);
  const profilePage = useAIPage(readProfiles, profiles);
  const policyRead = useResource(readPolicy);
  const grantPage = useAIPage(readGrants, grants);
  return { profiles, policies, grants, profilePage, policyRead, grantPage, readProfile, readPolicy, readGrant, policy: policies.get(workspaceId) };
}

export type AIData = ReturnType<typeof useAIData>;

export function useAIMutation<T>(metadata: AIMetadata<T>) {
  const action = useScopedAction();
  const run = useCallback((
    operation: (signal: AbortSignal) => Promise<T>,
    complete: (item: T) => void,
    failed?: (error: APIError) => void,
  ) => action.run(async (signal) => {
    const started = metadata.startWrite();
    return { item: await operation(signal), started };
  }, ({ item, started }) => { metadata.accept(item, started); complete(item); }, failed), [action.run, metadata]);
  return { pending: action.pending, error: action.error, run };
}
