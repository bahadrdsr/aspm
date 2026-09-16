import { APIError } from "@/api/client";

export const maxUploadBytes = 8 << 20;
const encoder = new TextEncoder();

export function formText(data: FormData, name: string): string {
  const value = data.get(name);
  if (typeof value !== "string") throw new APIError("A required form field is missing. Reopen the form and try again.", "invalid-input", false);
  return value;
}

export function inputChoice<T extends string>(value: string, allowed: readonly T[], label: string): T {
  const match = allowed.find((candidate) => candidate === value);
  if (match === undefined) throw new APIError(`Choose a supported ${label}.`, "invalid-input", false);
  return match;
}

export function inputText(value: string, label: string, maxBytes: number, allowEmpty = false): string {
  if ((!allowEmpty && value.trim() === "") || value.includes("\0") || encoder.encode(value).byteLength > maxBytes) {
    throw new APIError(`${label} is required and must fit within ${maxBytes} UTF-8 bytes${allowEmpty ? ", or be empty" : ""}.`, "invalid-input", false);
  }
  return value;
}

export function sourceTimestamp(value: string): string | null {
  if (value === "") return null;
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) || !Number.isFinite(Date.parse(value))) {
    throw new APIError("Source scan time must be RFC3339 with a timezone, or blank when unknown.", "invalid-input", false);
  }
  return value;
}

export function validateUploadSize(value: unknown): void {
  if (encoder.encode(JSON.stringify(value)).byteLength > maxUploadBytes) {
    throw new APIError("The encoded report request exceeds the 8 MiB upload limit. Use a smaller report.", "too-large", false);
  }
}

export function readReportFile(file: File | null, signal: AbortSignal): Promise<string> {
  if (!file || file.size === 0) return Promise.reject(new APIError("Select a nonempty UTF-8 report file.", "invalid-input", false));
  if (file.size > maxUploadBytes) return Promise.reject(new APIError("The report file exceeds the 8 MiB upload limit.", "too-large", false));
  const boundedSignal = AbortSignal.any([signal, AbortSignal.timeout(15_000)]);
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    const abort = () => {
      reader.onabort = null;
      reader.abort();
      finish(signal.aborted ? new DOMException("Report read cancelled", "AbortError") :
        new APIError("Reading the report timed out. Select a locally available file and try again.", "invalid-input", false));
    };
    const finish = (error: unknown, value?: string) => {
      boundedSignal.removeEventListener("abort", abort);
      reader.onload = reader.onerror = reader.onabort = null;
      if (error !== null) reject(error);
      else if (value !== undefined) resolve(value);
    };
    reader.onerror = () => finish(new APIError("The selected report file could not be read.", "invalid-input", false));
    reader.onabort = () => finish(new DOMException("Report read cancelled", "AbortError"));
    reader.onload = () => {
      if (!(reader.result instanceof ArrayBuffer)) {
        finish(new APIError("The report file did not contain readable bytes.", "invalid-input", false));
        return;
      }
      try {
        finish(null, new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(reader.result));
      } catch (error) {
        finish(error instanceof TypeError ? new APIError("The report must be valid UTF-8 text.", "invalid-input", false) : error);
      }
    };
    if (boundedSignal.aborted) { abort(); return; }
    boundedSignal.addEventListener("abort", abort, { once: true });
    reader.readAsArrayBuffer(file);
  });
}
