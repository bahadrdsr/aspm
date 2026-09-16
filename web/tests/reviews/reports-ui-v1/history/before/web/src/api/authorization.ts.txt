let workspace: string | null = null;
let revision = 0;
let controller = new AbortController();
const listeners = new Set<() => void>();

export function setRequestWorkspace(id: string | null): void {
  controller.abort();
  controller = new AbortController();
  workspace = id;
  revision += 1;
}

export function requestAuthority() {
  return { workspace, revision, signal: controller.signal };
}

export function rejectSession(expectedRevision: number): void {
  if (expectedRevision !== revision) return;
  setRequestWorkspace(null);
  for (const listener of listeners) listener();
}

export function onSessionRejected(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}
