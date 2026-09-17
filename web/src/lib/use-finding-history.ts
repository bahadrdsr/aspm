import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import type { FindingDetail, FindingNote, FindingResponse, Observation } from "@/api/types";
import { useResource } from "./use-resource";

export type FindingHistoryStream = "notes" | "observations";
interface HistoryRead {
  notesCursor: string;
  observationsCursor: string;
  target: FindingHistoryStream | null;
  sequence: number;
}
interface History<T> {
  items: Map<string, T>;
  ordered: T[] | undefined;
  supplied: boolean;
  frontier: string;
  next: string | null | undefined;
  paged: boolean;
}
function history<T>(): History<T> {
  return { items: new Map(), ordered: undefined, supplied: false, frontier: "", next: undefined, paged: false };
}
function merge<T extends { id: string }>(state: History<T>, items: readonly T[] | undefined) {
  if (items === undefined) return;
  let changed = !state.supplied;
  state.supplied = true;
  for (const item of items) {
    const previous = state.items.get(item.id);
    if (previous === undefined || JSON.stringify(previous) !== JSON.stringify(item)) {
      state.items.set(item.id, item);
      changed = true;
    }
  }
  if (changed) state.ordered = [...state.items.values()].sort((a, b) => a.id.localeCompare(b.id));
}
function advance<T>(state: History<T>, requested: string, next: string | null | undefined) {
  if (next === undefined || requested < state.frontier) return;
  state.frontier = requested;
  state.next = next;
  state.paged ||= requested !== "" || next !== null;
}

export function useFindingHistory(id: string) {
  const [read, setRead] = useState<HistoryRead>({ notesCursor: "", observationsCursor: "", target: null, sequence: 0 });
  const [acknowledgement, renderAcknowledgement] = useState(0);
  const notes = useRef(history<FindingNote>());
  const observations = useRef(history<Observation>());
  const canonical = useRef<FindingResponse | null>(null);
  const noteReceipts = useRef(new Map<string, { order: number; note: FindingNote }>());
  const ackOrder = useRef(0);
  const hasPatched = useRef(false);
  const scheduled = useRef(false);
  const mounted = useRef(true);
  const clear = useCallback(() => {
    notes.current = history();
    observations.current = history();
    canonical.current = null;
    noteReceipts.current.clear();
    hasPatched.current = false;
  }, []);
  useLayoutEffect(() => {
    mounted.current = true;
    const signal = requestAuthority().signal;
    signal.addEventListener("abort", clear, { once: true });
    return () => { mounted.current = false; signal.removeEventListener("abort", clear); clear(); };
  }, [clear]);
  const mergePages = useCallback((finding: FindingDetail, startedBefore: number) => {
    merge(notes.current, finding.notes?.map((note) => {
      const receipt = noteReceipts.current.get(note.id);
      return receipt && receipt.order > startedBefore ? receipt.note : note;
    }));
    merge(observations.current, finding.observations);
  }, []);
  const load = useCallback(async (signal: AbortSignal) => {
    scheduled.current = false;
    const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
    const startedBefore = ackOrder.current;
    let response: FindingResponse;
    try {
      response = await api.finding(id, scopedSignal, { notesCursor: read.notesCursor, observationsCursor: read.observationsCursor });
    } catch (cause: unknown) {
      scopedSignal.throwIfAborted();
      if (cause instanceof APIError && !["network", "unavailable"].includes(cause.code)) clear();
      throw cause;
    }
    scopedSignal.throwIfAborted();
    if (!mounted.current) throw new DOMException("Finding closed", "AbortError");
    mergePages(response.finding, startedBefore);
    advance(notes.current, read.notesCursor, response.finding.notesNextCursor);
    advance(observations.current, read.observationsCursor, response.finding.observationsNextCursor);
    // Only local request/ACK ordering is known, never a server revision or atomic snapshot.
    if (canonical.current === null || startedBefore >= ackOrder.current) canonical.current = response;
    return response;
  }, [id, read, clear, mergePages]);
  const resource = useResource(load);
  const data = useMemo(() => {
    const response = canonical.current;
    if (response === null) return null;
    return { ...response, finding: {
      ...response.finding,
      notes: notes.current.ordered,
      observations: observations.current.ordered,
      notesNextCursor: notes.current.next, observationsNextCursor: observations.current.next,
    } };
  }, [resource.data, resource.status, resource.error, acknowledgement]);
  function requireDetail() {
    if (canonical.current === null || !mounted.current) {
      throw new APIError("Finding details are withheld. Request an authorized read before continuing.", "forbidden", false);
    }
  }
  function acceptPatch(response: FindingResponse) {
    requireDetail();
    if (response.finding.id !== id || response.finding.workspaceId !== requestAuthority().workspace) {
      throw new APIError("The finding acknowledgement belongs to a different scope.", "invalid-response", false);
    }
    const order = ++ackOrder.current;
    canonical.current = response;
    mergePages(response.finding, order);
    hasPatched.current = true;
    // A capped mutation response merges immediately but must not move either GET frontier.
    renderAcknowledgement(order);
  }
  function addNote(note: FindingNote) {
    requireDetail();
    const order = ++ackOrder.current;
    noteReceipts.current.set(note.id, { order, note });
    merge(notes.current, [note]);
    renderAcknowledgement(order);
  }
  function loadMore(stream: FindingHistoryStream) {
    if (resource.status === "loading" || scheduled.current) return;
    const next = stream === "notes" ? notes.current.next : observations.current.next;
    if (next == null) return;
    scheduled.current = true;
    setRead((previous) => ({
      ...previous, target: stream, sequence: previous.sequence + 1,
      notesCursor: stream === "notes" ? next : previous.notesCursor,
      observationsCursor: stream === "observations" ? next : previous.observationsCursor,
    }));
  }
  function retry() {
    if (resource.status === "loading" || scheduled.current) return;
    scheduled.current = true;
    setRead((previous) => ({ ...previous, sequence: previous.sequence + 1 }));
  }
  return {
    data, error: resource.error, status: resource.status, pending: resource.status === "loading",
    target: read.target, hasPatched: hasPatched.current, acceptPatch, addNote, loadMore, retry,
    pages: {
      notes: { visible: notes.current.paged, next: notes.current.next },
      observations: { visible: observations.current.paged, next: observations.current.next },
    },
  };
}
