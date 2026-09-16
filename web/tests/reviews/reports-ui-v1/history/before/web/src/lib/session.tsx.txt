import { createContext, useContext, useEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { APIError, api } from "@/api/client";
import { onSessionRejected, setRequestWorkspace } from "@/api/authorization";
import type { Membership, Session } from "@/api/types";
import { Button } from "@/components/ui/button";
import { ActionButton } from "@/components/action-button";

interface SessionContextValue {
  session: Session;
  workspace: Membership;
  changeWorkspace: (id: string) => void;
  signOut: () => void;
}

const SessionContext = createContext<SessionContextValue | null>(null);

export function useSession(): SessionContextValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error("Protected view requires an authenticated session.");
  return value;
}

type State =
  | { kind: "checking" }
  | { kind: "signing-out" }
  | { kind: "signout-failed" }
  | { kind: "anonymous"; notice?: string }
  | { kind: "failed"; message: string }
  | { kind: "authenticated"; session: Session; workspace: Membership };

export function SessionBoundary({ children }: { children: ReactNode }) {
  const [state, setState] = useState<State>({ kind: "checking" });
  const [reload, setReload] = useState(0);
  const stateRef = useRef(state);
  stateRef.current = state;
  useEffect(() => onSessionRejected(() => {
    setState({ kind: "anonymous", notice: "Your session ended. Sign in to continue." });
  }), []);
  function enter(session: Session) {
    if (session.workspaces.length === 0) {
      setRequestWorkspace(null);
      setState({ kind: "failed", message: "No workspace membership is available. Ask an administrator to review your access." });
      return;
    }
    setRequestWorkspace(session.workspaces[0].id);
    setState({ kind: "authenticated", session, workspace: session.workspaces[0] });
  }
  useEffect(() => {
    const controller = new AbortController();
    setRequestWorkspace(null);
    setState({ kind: "checking" });
    void api.session(controller.signal).then(
      (session) => { if (!controller.signal.aborted) enter(session); },
      (error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof APIError && error.code === "unauthorized") setState({ kind: "anonymous" });
        else setState({ kind: "failed", message: error instanceof APIError ? error.message : "Session verification failed." });
      },
    );
    return () => { controller.abort(); setRequestWorkspace(null); };
  }, [reload]);
  useEffect(() => {
    if (state.kind !== "authenticated") return;
    let timer: ReturnType<typeof setTimeout>;
    const expiry = Date.parse(state.session.expiresAt);
    function expire() {
      const remaining = expiry - Date.now();
      if (remaining <= 0) {
        setRequestWorkspace(null);
        setState({ kind: "anonymous", notice: "Your session expired. Sign in to continue." });
      } else timer = setTimeout(expire, Math.min(remaining, 2_147_483_647));
    }
    expire();
    return () => clearTimeout(timer);
  }, [state]);
  async function signOut() {
    setRequestWorkspace(null);
    setState({ kind: "signing-out" });
    try {
      await api.logout();
      setState({ kind: "anonymous", notice: "Signed out." });
    } catch (error) {
      if (error instanceof APIError && error.code === "unauthorized") setState({ kind: "anonymous", notice: "Signed out." });
      else setState({ kind: "signout-failed" });
    }
  }
  function changeWorkspace(id: string) {
    const current = stateRef.current;
    if (current.kind !== "authenticated") return;
    const workspace = current.session.workspaces.find((item) => item.id === id);
    if (!workspace) {
      setRequestWorkspace(null);
      setState({ kind: "failed", message: "The selected workspace is not in the verified session." });
      return;
    }
    if (workspace.id === current.workspace.id) return;
    const route = new URL(window.location.hash.replace(/^#/, "") || "/work", window.location.origin);
    route.searchParams.delete("finding");
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}#${route.pathname}${route.search}`);
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    setRequestWorkspace(workspace.id);
    setState({ kind: "authenticated", session: current.session, workspace });
  }
  if (state.kind === "checking") return <div className="auth-shell"><section className="auth-card" role="status">Checking your session...</section></div>;
  if (state.kind === "signing-out") return <div className="auth-shell"><section className="auth-card" role="status">Signing out...</section></div>;
  if (state.kind === "signout-failed") return <div className="auth-shell"><section className="auth-card"><h1>Sign-out not confirmed</h1><p role="alert">Protected views have been cleared, but the server did not confirm session revocation.</p><ActionButton onClick={() => { void signOut(); }}>Retry sign out</ActionButton></section></div>;
  if (state.kind === "failed") return <div className="auth-shell"><section className="auth-card"><h1>Session unavailable</h1><p role="alert">{state.message}</p><ActionButton onClick={() => setReload((value) => value + 1)}>Retry session check</ActionButton></section></div>;
  if (state.kind === "anonymous") return <Authentication notice={state.notice} onAuthenticated={enter} />;
  return <SessionContext.Provider value={{ session: state.session, workspace: state.workspace, changeWorkspace, signOut }}>
    <div key={`${state.session.user.id}:${state.workspace.id}`}>{children}</div>
  </SessionContext.Provider>;
}

function Authentication({ notice, onAuthenticated }: { notice?: string; onAuthenticated: (session: Session) => void }) {
  const [setup, setSetup] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [message, setMessage] = useState<string | undefined>(notice);
  const [pending, setPending] = useState(false);
  const requestRef = useRef<AbortController | null>(null);
  useEffect(() => () => requestRef.current?.abort(), []);
  useEffect(() => setMessage(notice), [notice]);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    const controller = new AbortController();
    requestRef.current?.abort();
    requestRef.current = controller;
    setPending(true);
    setError(null);
    try {
      const email = String(values.get("email") ?? "");
      const password = String(values.get("password") ?? "");
      if (setup) {
        await api.bootstrap({
          workspaceName: String(values.get("workspaceName") ?? ""),
          name: String(values.get("name") ?? ""), email, password,
        }, String(values.get("bootstrapToken") ?? ""), controller.signal);
        if (!controller.signal.aborted) {
          form.reset();
          setSetup(false);
          setMessage("Instance set up. Sign in with the account you created.");
        }
      } else {
        const session = await api.login(email, password, controller.signal);
        if (!controller.signal.aborted) { form.reset(); onAuthenticated(session); }
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause instanceof APIError ? cause.message : "The request could not be completed.");
    } finally {
      for (const name of ["password", "bootstrapToken"]) {
        const field = form.elements.namedItem(name);
        if (field instanceof HTMLInputElement) field.value = "";
      }
      if (!controller.signal.aborted) setPending(false);
    }
  }
  const title = setup ? "Set up instance" : "Sign in";
  return <main className="auth-shell">
    <section className="auth-card">
      <p className="eyebrow">aspm / application security</p>
      <h1>{title}</h1>
      <p className="muted">{setup ? "Use the out-of-band enrollment token supplied by your operator. No shared default account is created." : "Use your instance account. Credentials are sent only to this application."}</p>
      {message && <p role="status" className="auth-notice">{message}</p>}
      {error && <p role="alert" className="auth-error">{error}</p>}
      <form aria-label={title} onSubmit={(event) => { void submit(event); }}>
        {setup && <><label>Workspace name<input name="workspaceName" required autoComplete="organization" /></label><label>Name<input name="name" required autoComplete="name" /></label></>}
        <label>Email<input name="email" type="email" required autoComplete="username" /></label>
        <label>Password<input name="password" type="password" required autoComplete={setup ? "new-password" : "current-password"} /></label>
        {setup && <label>Bootstrap token<input name="bootstrapToken" type="password" required autoComplete="off" /></label>}
        <ActionButton type="submit" disabled={pending}>{pending ? "Please wait..." : title}</ActionButton>
      </form>
      <Button variant="ghost" disabled={pending} onClick={() => { setSetup(!setup); setError(null); setMessage(undefined); }}>{setup ? "Back to sign in" : "Set up instance"}</Button>
    </section>
  </main>;
}
