import { APIError } from "./client";
import { aiFamilies } from "./ai-types";
import type { AIFamily, AIProfile, AIProfileInput, AIProfilePatch } from "./ai-types";
import { inputText } from "@/lib/application-input";

const encoder = new TextEncoder();

function privateIPv4(host: string): boolean {
  if (!/^(?:0|[1-9]\d{0,2})(?:\.(?:0|[1-9]\d{0,2})){3}$/.test(host)) return false;
  const octets = host.split(".").map(Number);
  if (octets.some((value) => value > 255)) return false;
  const [a, b] = octets;
  return a === 127 || a === 10 || a === 172 && b >= 16 && b <= 31 || a === 192 && b === 168;
}

function privateLiteral(host: string): boolean {
  if (!host.startsWith("[")) return privateIPv4(host);
  if (!/^\[[a-fA-F0-9:.]+\]$/.test(host)) return false;
  try {
    const address = new URL(`http://${host}`).hostname.slice(1, -1);
    if (address === "::1") return true;
    const first = Number.parseInt(address.split(":")[0], 16);
    if (first >= 0xfc00 && first <= 0xfdff) return true;
    const mapped = /^::ffff:([a-f0-9]{1,4}):([a-f0-9]{1,4})$/.exec(address);
    if (!mapped) return false;
    const high = Number.parseInt(mapped[1], 16), low = Number.parseInt(mapped[2], 16);
    return privateIPv4(`${high >> 8}.${high & 255}.${low >> 8}.${low & 255}`);
  } catch { return false; }
}

export function validAIEndpoint(value: string, family: AIFamily): boolean {
  if (!value || value !== value.trim() || encoder.encode(value).byteLength > 16384 || /[\p{Cc}\\?#]/u.test(value)) return false;
  const parts = /^(https?):\/\/([^/]+)(\/.*)?$/.exec(value);
  if (!parts || /[@\s]/u.test(parts[2])) return false;
  const authority = /^(\[[^\]]+\]|[^:]+)(?::([0-9]+))?$/.exec(parts[2]);
  if (!authority || authority[2] !== undefined && (Number(authority[2]) < 1 || Number(authority[2]) > 65535)) return false;
  try {
    const path = parts[3] ?? "";
    const decoded = decodeURIComponent(path);
    if (/[\p{Cc}\\]/u.test(decoded) || /%2f|%5c/i.test(path) || decoded.split("/").some((part) => part === "." || part === "..")) return false;
    const url = new URL(value);
    if (!url.hostname || url.username || url.password || url.search || url.hash) return false;
    return url.protocol === "https:" || family === "local" && url.protocol === "http:" && privateLiteral(authority[1]);
  } catch { return false; }
}

export function aiBodyLimit(body: object): void {
  if (encoder.encode(JSON.stringify(body)).byteLength > 32 << 10) {
    throw new APIError("The encoded AI configuration request exceeds 32 KiB. Shorten the input and retry.", "too-large", false);
  }
}

export function validateAIProfile(input: AIProfileInput | AIProfilePatch, original?: AIProfile): void {
  if (Object.keys(input).some((field) => !["name", "family", "endpoint", "model", "deployment", "enabled", "structuredOutput", "apiKey"].includes(field))) {
    throw new APIError("Only explicit AI profile configuration fields may be changed.", "invalid-input", false);
  }
  const combined = { ...original, ...input };
  if (!combined.family || !aiFamilies.some((family) => family === combined.family) ||
    combined.name === undefined || combined.model === undefined || combined.deployment === undefined ||
    combined.endpoint === undefined || typeof combined.enabled !== "boolean" || typeof combined.structuredOutput !== "boolean") {
    throw new APIError("Choose a family, enabled state and structured-output review, and complete the profile fields.", "invalid-input", false);
  }
  inputText(combined.name, "Name", 256);
  inputText(combined.model, "Model", 256);
  inputText(combined.deployment, "Deployment", 256, combined.family !== "azure-foundry");
  if (combined.family !== "azure-foundry" && combined.deployment !== "") {
    throw new APIError("Only Azure Foundry profiles may have a deployment. Model and deployment are separate choices.", "invalid-input", false);
  }
  if (!validAIEndpoint(combined.endpoint, combined.family)) {
    throw new APIError("Enter a valid HTTPS endpoint without credentials, query, fragment, controls or unsafe paths. Local HTTP requires a literal private or loopback IP, not localhost. Endpoints are limited to 16384 UTF-8 bytes.", "invalid-input", false);
  }
  if (input.apiKey !== undefined && input.apiKey !== null) {
    inputText(input.apiKey, "API key", 16384);
    if (/\p{Cc}/u.test(input.apiKey)) throw new APIError("API keys cannot contain control characters.", "invalid-input", false);
  }
  const hasKey = input.apiKey === undefined ? original?.credentialConfigured === true : input.apiKey !== null;
  if (combined.family !== "local" && !hasKey) {
    throw new APIError("Hosted profiles require an API key. Only local profiles may clear or omit the stored key.", "invalid-input", false);
  }
  aiBodyLimit(input);
}

export function utcExpiry(value: string): string | null {
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d(?::\d\d(?:\.\d{1,3})?)?$/.test(value)) return null;
  const date = new Date(`${value}Z`), time = date.getTime();
  if (!Number.isFinite(time)) return null;
  const result = date.toISOString();
  if (result.slice(0, 16) !== value.slice(0, 16) || time <= Date.now()) return null;
  return result;
}
