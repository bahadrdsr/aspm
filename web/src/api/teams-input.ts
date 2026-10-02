import { APIError } from "./client";
import type { TeamsDestination, TeamsMetadata, TeamsPayload } from "./teams-types";

const encoder = new TextEncoder();
export const teamsNativeID = /^[a-f0-9]{32}$/;
export const teamsDigest = /^sha256:[a-f0-9]{64}$/;
export const teamsBytes = (value: string) => encoder.encode(value).byteLength;
const reject = (message: string): never => { throw new APIError(message, "invalid-input", false); };

export function validateTeamsName(value: string) {
  if (/^\p{White_Space}*$/u.test(value) || value.includes("\0") || teamsBytes(value) > 256) {
    reject("Name must be nonblank, NUL-free and at most 256 UTF-8 bytes. Your draft is unchanged.");
  }
}
export function canonicalTeamsOrigin(value: string): boolean {
  if (!value.startsWith("https://") || teamsBytes(value) > 16384) return false;
  const host = value.slice(8);
  if (!host || host !== host.toLowerCase() || /[/\\%?#@\p{Cc}\p{White_Space}]/u.test(host)) return false;
  const parts = /^(\[[0-9a-f:.]+\]|[^:]+)(?::([1-9][0-9]{0,4}))?$/.exec(host);
  if (!parts || parts[1].endsWith(".") || parts[2] &&
    (Number(parts[2]) === 443 || Number(parts[2]) > 65535)) return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && !!parsed.hostname && !parsed.username && !parsed.password &&
      parsed.pathname === "/" && !parsed.search && !parsed.hash;
  } catch { return false; }
}
function decode(value: string, query = false) {
  const bytes: number[] = [];
  for (let index = 0; index < value.length;) {
    if (value[index] === "%") {
      const hex = value.slice(index + 1, index + 3);
      if (!/^[a-f0-9]{2}$/i.test(hex)) throw new Error("Invalid escape");
      bytes.push(parseInt(hex, 16)); index += 3;
    } else {
      const character = String.fromCodePoint(value.codePointAt(index)!);
      bytes.push(...encoder.encode(query && character === "+" ? " " : character));
      index += character.length;
    }
  }
  return { bytes: bytes.length, text: new TextDecoder().decode(new Uint8Array(bytes)) };
}
export function validateTeamsWorkflow(value: string): string {
  try {
    if (!value || value.trim() !== value || teamsBytes(value) > 16384 || /[\p{Cc}#]/u.test(value)) throw new Error();
    const parts = /^(https:\/\/[^/]+)(\/[^?]*)(?:\?(.*))?$/s.exec(value);
    if (!parts || !canonicalTeamsOrigin(parts[1])) throw new Error();
    // Check original routing bytes before any WHATWG URL dot-segment normalization.
    const path = decode(parts[2]);
    if (path.bytes > 4096 || path.text === "/" || /[\\%?#\p{Cc}]/u.test(path.text) ||
      path.text.includes("//") || path.text.split("/").some((part) => part === "." || part === "..") ||
      /%2f|%5c/i.test(parts[2])) throw new Error();
    let signatures = 0;
    for (const pair of (parts[3] ?? "").split("&").filter(Boolean)) {
      if (pair.includes(";")) throw new Error();
      const equal = pair.indexOf("="), key = decode(equal < 0 ? pair : pair.slice(0, equal), true).text;
      const item = decode(equal < 0 ? "" : pair.slice(equal + 1), true).text;
      if (/[\p{Cc}]/u.test(key + item)) throw new Error();
      if (key === "sig") {
        signatures++;
        if (!item || /\p{White_Space}/u.test(item)) throw new Error();
      }
    }
    if (signatures !== 1) throw new Error();
    return parts[1];
  } catch {
    return reject("Enter a canonical signed HTTPS Workflow URL within the native URL/path byte limits, with one nonempty signature and safe routing. The private draft is unchanged.");
  }
}
export function sameTeamsMetadata(a: TeamsMetadata, b: TeamsMetadata) {
  return a.workflowOrigin === b.workflowOrigin && a.channelType === b.channelType &&
    a.ownershipAcknowledged === b.ownershipAcknowledged;
}
export function sameTeamsDestination(a: TeamsDestination, b: TeamsDestination) {
  return a.name === b.name && sameTeamsMetadata(a, b);
}
export function sameTeamsPayload(a: TeamsPayload, b: TeamsPayload) {
  return a.title === b.title && a.body === b.body && a.deepLink === b.deepLink;
}
