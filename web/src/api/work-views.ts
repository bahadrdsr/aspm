import { APIError, request, workSearchQuery } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import { inputChoice, inputText } from "@/lib/application-input";

export const workViewSorts = ["source-order", "severity", "title"] as const;
export type WorkViewSort = typeof workViewSorts[number];
export interface WorkViewFields { name: string; query: string; sort: WorkViewSort }
export interface SavedWorkView extends WorkViewFields {
  id: string; revision: string; createdAt: string; updatedAt: string;
}
export interface SavedWorkViewsPage {
  apiVersion: typeof apiVersion; items: SavedWorkView[]; total: number; nextCursor: string | null;
}
export type WorkViewPatch = { revision: string } & Partial<WorkViewFields>;

const path = "/api/v1/work/views";
const encoder = new TextEncoder();
const fields = ["id", "name", "query", "sort", "revision", "createdAt", "updatedAt"] as const;

function invalid(field: string): never {
  throw new APIError(`The service returned invalid saved view ${field}. No new metadata or successful change was confirmed.`, "invalid-response", false);
}
function record(value: unknown, keys: readonly string[], field: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).length !== keys.length || Object.keys(value).some((key) => !keys.includes(key))) return invalid(field);
  return value as Record<string, unknown>;
}
function envelope(value: unknown, keys: readonly string[]) {
  const body = record(value, ["apiVersion", ...keys], "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  return body;
}
function identifier(value: unknown): string {
  if (typeof value !== "string" || !/^[a-f0-9]{32}$/.test(value)) return invalid("identifier or cursor");
  return value;
}
function text(value: unknown, maximum: number, empty = false): string {
  if (typeof value !== "string" || value !== value.trim() || (!empty && value === "") ||
    value.includes("\0") || encoder.encode(value).byteLength > maximum) return invalid("name or query");
  return value;
}
function revision(value: unknown): string {
  if (typeof value !== "string" || !/^[1-9][0-9]{0,18}$/.test(value) || BigInt(value) > 9223372036854775807n) {
    return invalid("revision");
  }
  return value;
}
function timestamp(value: unknown): string {
  if (typeof value !== "string") return invalid("timestamp");
  const match = /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d{1,9})?(?:Z|([+-])(\d\d):(\d\d))$/.exec(value);
  if (!match || !Number.isFinite(Date.parse(value))) return invalid("timestamp");
  const [, year, month, day, hour, minute, second, , offsetHour = "0", offsetMinute = "0"] = match;
  const days = new Date(Date.UTC(Number(year), Number(month), 0)).getUTCDate();
  if (Number(month) < 1 || Number(month) > 12 || Number(day) < 1 || Number(day) > days ||
    Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59 || Number(offsetHour) > 23 || Number(offsetMinute) > 59) {
    return invalid("timestamp");
  }
  return value;
}
function view(value: unknown): SavedWorkView {
  const item = record(value, fields, "fields");
  const createdAt = timestamp(item.createdAt), updatedAt = timestamp(item.updatedAt);
  if (Date.parse(updatedAt) < Date.parse(createdAt)) return invalid("timestamp order");
  const sort = workViewSorts.find((sort) => sort === item.sort);
  if (sort === undefined) return invalid("loaded sort");
  return {
    id: identifier(item.id), name: text(item.name, 256), query: text(item.query, 512, true), sort,
    revision: revision(item.revision), createdAt, updatedAt,
  };
}
export function parseSavedWorkView(value: unknown, id?: string): SavedWorkView {
  const result = view(envelope(value, ["view"]).view);
  if (id !== undefined && result.id !== id) return invalid("selected identifier");
  return result;
}
export function parseSavedWorkViews(value: unknown, cursor: string | null): SavedWorkViewsPage {
  const body = envelope(value, ["items", "total", "nextCursor"]);
  if (!Array.isArray(body.items) || body.items.length > 100) return invalid("page size");
  const items = body.items.map(view), total = body.total;
  const nextCursor = body.nextCursor === null ? null : identifier(body.nextCursor);
  if (typeof total !== "number" || !Number.isSafeInteger(total) || total < items.length ||
    items.some((item, index) => item.id <= (index === 0 ? cursor ?? "" : items[index - 1].id)) ||
    nextCursor !== null && (nextCursor <= (cursor ?? "") || nextCursor !== items.at(-1)?.id || total <= items.length)) {
    return invalid("page order, count or cursor");
  }
  return { apiVersion, items, total, nextCursor };
}
export function workViewName(draft: string): string {
  return inputText(draft.trim(), "View name", 256);
}
export function workViewFields(input: WorkViewFields): WorkViewFields {
  return {
    name: workViewName(input.name), query: workSearchQuery(input.query),
    sort: inputChoice(input.sort, workViewSorts, "loaded sort"),
  };
}
export function isWorkViewDenied(error: unknown): boolean {
  return error instanceof APIError && (error.code === "forbidden" || error.code === "not-found" ||
    error.httpStatus === 403 || error.httpStatus === 404);
}
function safeFailure(cause: unknown): never {
  if (!(cause instanceof APIError)) throw cause;
  let message: string;
  switch (cause.code) {
    case "unauthorized": message = "Your session ended. Sign in to continue."; break;
    case "forbidden": message = "Personal saved view metadata is denied in this workspace. Refresh an authorized list before continuing."; break;
    case "not-found": message = "This personal saved view is missing or no longer available to you. Its private metadata is withheld."; break;
    case "conflict": message = "The saved view changed. Reload saved view to review its current revision before submitting changes again."; break;
    case "network":
    case "unavailable": message = "Saved views are unavailable. Retry the exact read or refresh for current authorized metadata."; break;
    case "invalid-response": message = "The service returned invalid saved view metadata or an unexpected receipt status. No new metadata or successful change was confirmed."; break;
    default: message = "The service rejected this saved view request. Check the name, query and loaded sort limits.";
  }
  throw new APIError(message, cause.code, cause.retryable, null, cause.httpStatus);
}
function detailPath(id: string): string { return `${path}/${identifier(id)}`; }
async function read<T>(url: string, parse: (value: unknown) => T, signal: AbortSignal): Promise<T> {
  const scoped = AbortSignal.any([signal, requestAuthority().signal]);
  await Promise.resolve();
  scoped.throwIfAborted();
  return request(url, parse, { signal: scoped, expectedStatus: 200 }).catch(safeFailure);
}
function receipt(value: unknown, input: WorkViewFields, original?: SavedWorkView): SavedWorkView {
  const result = parseSavedWorkView(value, original?.id);
  if (result.name !== input.name || result.query !== input.query || result.sort !== input.sort ||
    (!original && result.revision !== "1") || original &&
    (BigInt(result.revision) <= BigInt(original.revision) || result.createdAt !== original.createdAt ||
      Date.parse(result.updatedAt) < Date.parse(original.updatedAt))) return invalid("canonical receipt");
  return result;
}
export const workViewsApi = {
  list: (cursor: string | null, signal: AbortSignal) => read(
    cursor === null ? path : `${path}?${new URLSearchParams({ limit: "100", cursor: identifier(cursor) })}`,
    (value) => parseSavedWorkViews(value, cursor), signal),
  detail: (id: string, signal: AbortSignal) => read(detailPath(id), (value) => parseSavedWorkView(value, id), signal),
  create: (input: WorkViewFields, signal: AbortSignal) => {
    const body = workViewFields(input);
    return request(path, (value) => receipt(value, body), { method: "POST", body, signal, expectedStatus: 201 }).catch(safeFailure);
  },
  update: (original: SavedWorkView, input: WorkViewFields, signal: AbortSignal) => {
    const next = workViewFields(input);
    const body: WorkViewPatch = {
      revision: revision(original.revision),
      ...(next.name !== original.name && { name: next.name }), ...(next.query !== original.query && { query: next.query }),
      ...(next.sort !== original.sort && { sort: next.sort }),
    };
    return request(detailPath(original.id), (value) => receipt(value, next, original),
      { method: "PATCH", body, signal, expectedStatus: 200 }).catch(safeFailure);
  },
  delete: (original: SavedWorkView, signal: AbortSignal) => request(detailPath(original.id), (value, status) => {
    if (status !== 204 || value !== null) return invalid("delete receipt");
  }, { method: "DELETE", body: { revision: revision(original.revision) }, signal, expectedStatus: 204 }).catch(safeFailure),
};
