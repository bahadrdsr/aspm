import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";

export function useScopedAction() {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<APIError | null>(null);
  const request = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  useLayoutEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; request.current?.abort(); };
  }, []);
  const run = useCallback(async <T,>(
    operation: (signal: AbortSignal) => Promise<T>,
    complete: (result: T) => void,
    failed?: (error: APIError) => void,
  ) => {
    if (request.current) return;
    const authority = requestAuthority();
    const controller = new AbortController();
    const signal = AbortSignal.any([controller.signal, authority.signal]);
    const current = () => mounted.current && request.current === controller &&
      !signal.aborted && requestAuthority().revision === authority.revision;
    request.current = controller;
    setPending(true);
    setError(null);
    try {
      if (!authority.workspace) throw new APIError("Sign in to continue.", "unauthorized", false);
      const result = await operation(signal);
      if (current()) complete(result);
    } catch (cause) {
      if (current()) {
        const error = cause instanceof APIError ? cause :
          new APIError("The action could not be confirmed. Refresh the current view before trying again.", "unavailable", false);
        setError(error);
        failed?.(error);
      }
    } finally {
      if (current()) setPending(false);
      if (request.current === controller) request.current = null;
    }
  }, []);
  return { run, pending, error };
}
