import { useCallback, useEffect, useState } from "react";
import { APIError } from "@/api/client";

type Resource<T> =
  | { status: "loading"; data: T | null; error: null }
  | { status: "ready"; data: T; error: null }
  | { status: "error"; data: T | null; error: APIError };

export function useResource<T>(load: (signal: AbortSignal) => Promise<T>) {
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<Resource<T>>({ status: "loading", data: null, error: null });
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setState((previous) => ({ status: "loading", data: previous.data, error: null }));
    void load(controller.signal).then(
      (data) => { if (current) setState({ status: "ready", data, error: null }); },
      (cause: unknown) => {
        if (!current || controller.signal.aborted) return;
        const error = cause instanceof APIError ? cause : new APIError("Unable to load this view. Please retry.", "unavailable", true);
        setState((previous) => ({
          status: "error",
          data: error.retryable && (error.code === "network" || error.code === "unavailable") ? previous.data : null,
          error,
        }));
      },
    );
    return () => { current = false; controller.abort(); };
  }, [load, revision]);
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  return { ...state, reload };
}
