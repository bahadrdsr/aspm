import { APIError } from "./client";
import { jiraFieldSources } from "./jira-types";
import type { JiraFieldSource, JiraPayload, JiraTarget } from "./jira-types";

const encoder = new TextEncoder();
const cloudID = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/;
export const jiraCustomField = /^customfield_[0-9]+$/;
export const jiraNativeID = /^[a-f0-9]{32}$/;
export const jiraDigest = /^sha256:[a-f0-9]{64}$/;
export const utf8Size = (value: string) => encoder.encode(value).byteLength;

function reject(message: string): never { throw new APIError(message, "invalid-input", false); }

export function validateJiraName(value: string) {
  if (/^\p{White_Space}*$/u.test(value) || value.includes("\0") || utf8Size(value) > 256) {
    reject("Name must be nonblank, NUL-free and at most 256 UTF-8 bytes. Your draft has not been changed.");
  }
}

export function validateJiraToken(value: string) {
  if (!value || /[\p{White_Space}\p{Cc}]/u.test(value) || utf8Size(value) > 16384) {
    reject("Enter an opaque OAuth bearer token of at most 16384 UTF-8 bytes, without whitespace or control characters.");
  }
}

export function canonicalJiraURL(value: string, path: string): boolean {
  if (!value.startsWith("https://") || utf8Size(value) > 16384 ||
    /[\p{Cc}\p{White_Space}\\%?#]/u.test(value) || !value.endsWith(path)) return false;
  const host = value.slice(8, path === "" ? undefined : -path.length);
  if (!host || host.includes("/") || host.includes("@") || host !== host.toLowerCase()) return false;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && !!url.hostname && !url.username && !url.password &&
      !url.search && !url.hash && url.pathname === (path || "/");
  } catch { return false; }
}

export function validateJiraTarget(value: JiraTarget) {
  if (value.credentialType !== "oauth2-bearer" || !cloudID.test(value.cloudId)) {
    reject("Cloud ID must be a lowercase UUID. This profile uses Jira Cloud v3 with OAuth bearer credentials.");
  }
  if (!canonicalJiraURL(value.siteOrigin, "")) {
    reject("Site origin must be a canonical HTTPS origin, without a path, credentials, query or fragment.");
  }
  if (!canonicalJiraURL(value.apiBase, `/ex/jira/${value.cloudId}`)) {
    reject("API base must be an approved HTTPS host with exactly /ex/jira/{Cloud ID}, without credentials, encoded routing, query, fragment or trailing slash.");
  }
  if (!/^[A-Z][A-Z0-9_]{0,254}$/.test(value.project)) {
    reject("Project must start with A-Z and contain at most 255 uppercase letters, digits or underscores.");
  }
  if (!/^[1-9][0-9]{0,19}$/.test(value.issueType)) {
    reject("Issue type must be a positive numeric ID of at most 20 digits.");
  }
  if (Object.keys(value.fieldMappings).length > 16) reject("At most 16 custom field mappings are permitted.");
  for (const [id, source] of Object.entries(value.fieldMappings)) {
    if (!jiraCustomField.test(id) || utf8Size(id) > 128 ||
      !jiraFieldSources.some((choice) => choice === source)) {
      reject("Each mapping needs a customfield_ numeric ID of at most 128 UTF-8 bytes and one of the five fixed string sources.");
    }
  }
}

export function jiraMappings(rows: readonly { id: string; source: JiraFieldSource }[]): Record<string, JiraFieldSource> {
  if (rows.length > 16) reject("At most 16 custom field mappings are permitted.");
  const fields: Record<string, JiraFieldSource> = {};
  for (const row of rows) {
    if (!jiraCustomField.test(row.id) || utf8Size(row.id) > 128 || Object.hasOwn(fields, row.id) ||
      !jiraFieldSources.some((choice) => choice === row.source)) {
      reject("Custom field IDs must be unique customfield_ numeric IDs, at most 128 UTF-8 bytes. Reserved fields, free paths and templates are not allowed.");
    }
    fields[row.id] = row.source;
  }
  return fields;
}

function sameFields(a: Record<string, string>, b: Record<string, string>) {
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((key) => a[key] === b[key] && Object.hasOwn(b, key));
}
export function sameJiraTarget(a: JiraTarget, b: JiraTarget) {
  return a.credentialType === b.credentialType && a.cloudId === b.cloudId && a.apiBase === b.apiBase &&
    a.siteOrigin === b.siteOrigin && a.project === b.project && a.issueType === b.issueType &&
    sameFields(a.fieldMappings, b.fieldMappings);
}
export function sameJiraPayload(a: JiraPayload, b: JiraPayload) {
  return a.title === b.title && a.body === b.body && a.deepLink === b.deepLink && sameFields(a.fields, b.fields);
}
