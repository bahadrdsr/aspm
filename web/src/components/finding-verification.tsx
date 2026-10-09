import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type {
  DeterministicVerification, DeterministicVerificationApproval, DeterministicVerificationEvidence,
  DeterministicVerificationPage, FindingDetail, VerificationApprovalPage, VerificationEvidencePage,
} from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Button } from "./ui/button";
import "./finding-verification.css";

interface PageState<T> {
  data: T | null;
  pending: boolean;
  error: APIError | null;
}

interface DetailState {
  id: string | null;
  data: DeterministicVerification | null;
  pending: boolean;
  denied: boolean;
  error: APIError | null;
}

interface PendingIntent {
  approvalId: string;
  idempotencyKey: string;
  knownIds: Set<string>;
}

function actionError(cause: unknown, message: string): APIError {
  return cause instanceof APIError ? cause : new APIError(message, "unavailable", true);
}

function replaceByID<T extends { id: string }>(items: T[], value: T): T[] {
  return [...new Map([...items, value].map((item) => [item.id, item])).values()]
    .sort((left, right) => left.id.localeCompare(right.id));
}

export function FindingVerification({ finding }: { finding: FindingDetail }) {
  const { workspace } = useSession();
  const canWrite = workspace.role !== "viewer";
  const canApprove = workspace.role === "admin";
  const [open, setOpen] = useState(false);
  const [evidence, setEvidence] = useState<PageState<VerificationEvidencePage>>({
    data: null, pending: false, error: null,
  });
  const [approvals, setApprovals] = useState<PageState<VerificationApprovalPage>>({
    data: null, pending: false, error: null,
  });
  const [history, setHistory] = useState<PageState<DeterministicVerificationPage>>({
    data: null, pending: false, error: null,
  });
  const [detail, setDetail] = useState<DetailState>({
    id: null, data: null, pending: false, denied: false, error: null,
  });
  const [environment, setEnvironment] = useState("synthetic-browser-environment");
  const [scopeRevision, setScopeRevision] = useState("synthetic-scope-v1");
  const [condition, setCondition] = useState("true");
  const [evidenceID, setEvidenceID] = useState("");
  const [approvalID, setApprovalID] = useState("");
  const [approvalExpiry, setApprovalExpiry] = useState("");
  const [approvalRationale, setApprovalRationale] = useState("");
  const [revocationRationale, setRevocationRationale] = useState("");
  const [actionPending, setActionPending] = useState(false);
  const [actionErrorState, setActionError] = useState<APIError | null>(null);
  const [unresolved, setUnresolved] = useState<PendingIntent | null>(null);
  const unresolvedRef = useRef(unresolved);
  unresolvedRef.current = unresolved;
  const requests = useRef(new Set<AbortController>());
  const environmentField = useId();
  const scopeField = useId();
  const conditionField = useId();
  const evidenceField = useId();
  const expiryField = useId();
  const rationaleField = useId();
  const approvalField = useId();
  const revocationField = useId();

  const currentApprovals = useMemo(
    () => approvals.data?.items.filter((item) => item.current) ?? [],
    [approvals.data],
  );

  useEffect(() => {
    if (!evidence.data?.items.some((item) => item.id === evidenceID)) {
      setEvidenceID(evidence.data?.items[0]?.id ?? "");
    }
  }, [evidence.data, evidenceID]);
  useEffect(() => {
    if (!currentApprovals.some((item) => item.id === approvalID)) {
      setApprovalID(currentApprovals[0]?.id ?? "");
    }
  }, [approvalID, currentApprovals]);
  useEffect(() => () => {
    for (const controller of requests.current) controller.abort();
    requests.current.clear();
  }, []);

  function controller() {
    const value = new AbortController();
    requests.current.add(value);
    value.signal.addEventListener("abort", () => requests.current.delete(value), { once: true });
    return value;
  }

  function initialRead() {
    const evidenceRequest = controller(), approvalRequest = controller(), historyRequest = controller();
    setEvidence((previous) => ({ ...previous, pending: true, error: null }));
    setApprovals((previous) => ({ ...previous, pending: true, error: null }));
    setHistory((previous) => ({ ...previous, pending: true, error: null }));
    void api.verificationEvidence(finding.id, 100, null, evidenceRequest.signal).then(
      (page) => { if (!evidenceRequest.signal.aborted) setEvidence({ data: page, pending: false, error: null }); },
      (cause: unknown) => {
        if (!evidenceRequest.signal.aborted) {
          const error = actionError(cause, "Unable to load verification evidence.");
          setEvidence((previous) => ({
            data: ["forbidden", "not-found"].includes(error.code) ? null : previous.data,
            pending: false, error,
          }));
        }
      },
    ).finally(() => requests.current.delete(evidenceRequest));
    void api.verificationApprovals(finding.id, 100, null, approvalRequest.signal).then(
      (page) => { if (!approvalRequest.signal.aborted) setApprovals({ data: page, pending: false, error: null }); },
      (cause: unknown) => {
        if (!approvalRequest.signal.aborted) {
          const error = actionError(cause, "Unable to load verification approvals.");
          setApprovals((previous) => ({
            data: ["forbidden", "not-found"].includes(error.code) ? null : previous.data,
            pending: false, error,
          }));
        }
      },
    ).finally(() => requests.current.delete(approvalRequest));
    void api.deterministicVerifications(finding.id, 100, null, historyRequest.signal).then(
      (page) => {
        if (historyRequest.signal.aborted) return;
        setHistory({ data: page, pending: false, error: null });
        const pending = unresolvedRef.current;
        const match = pending && page.items.find((item) =>
          item.approvalId === pending.approvalId && !pending.knownIds.has(item.id));
        if (match) {
          setUnresolved(null);
          setActionError(null);
          setDetail({ id: match.id, data: match, pending: false, denied: false, error: null });
        }
      },
      (cause: unknown) => {
        if (!historyRequest.signal.aborted) {
          const error = actionError(cause, "Unable to load verification history.");
          setHistory((previous) => ({
            data: ["forbidden", "not-found"].includes(error.code) ? null : previous.data,
            pending: false, error,
          }));
        }
      },
    ).finally(() => requests.current.delete(historyRequest));
  }

  useEffect(() => {
    if (!open) return;
    initialRead();
    return () => {
      for (const request of requests.current) request.abort();
      requests.current.clear();
    };
  // finding identity changes unmount this component in the current dialog.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  function openPane() {
    setOpen(true);
    setActionError(null);
  }

  async function submitEvidence(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (actionPending) return;
    const request = controller();
    setActionPending(true);
    setActionError(null);
    try {
      const response = await api.submitVerificationEvidence(finding.id, {
        environmentId: environment, scopeRevision, condition: condition === "true",
      }, request.signal);
      if (request.signal.aborted) return;
      setEvidence((previous) => {
        const items = replaceByID(previous.data?.items ?? [], response.evidence);
        return {
          data: {
            apiVersion: response.apiVersion, dataOrigin: response.dataOrigin, items,
            total: Math.max(previous.data?.total ?? 0, items.length), nextCursor: previous.data?.nextCursor ?? null,
          },
          pending: false, error: null,
        };
      });
      setEvidenceID(response.evidence.id);
    } catch (cause) {
      if (!request.signal.aborted) setActionError(actionError(cause, "Synthetic fixture submission was not confirmed."));
    } finally {
      requests.current.delete(request);
      if (!request.signal.aborted) setActionPending(false);
    }
  }

  async function approveEvidence(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (actionPending || evidenceID === "") return;
    const request = controller();
    setActionPending(true);
    setActionError(null);
    try {
      const response = await api.approveVerificationEvidence(finding.id, {
        evidenceId: evidenceID, rationale: approvalRationale,
        expiresAt: new Date(approvalExpiry).toISOString().replace(".000Z", "Z"),
      }, request.signal);
      if (request.signal.aborted) return;
      setApprovals((previous) => {
        const items = replaceByID(previous.data?.items ?? [], response.approval);
        return {
          data: {
            apiVersion: response.apiVersion, dataOrigin: response.dataOrigin, items,
            total: Math.max(previous.data?.total ?? 0, items.length), nextCursor: previous.data?.nextCursor ?? null,
          },
          pending: false, error: null,
        };
      });
      setApprovalID(response.approval.id);
      setApprovalRationale("");
    } catch (cause) {
      if (!request.signal.aborted) setActionError(actionError(cause, "Verification approval was not confirmed."));
    } finally {
      requests.current.delete(request);
      if (!request.signal.aborted) setActionPending(false);
    }
  }

  async function revokeApproval() {
    if (actionPending || approvalID === "") return;
    const request = controller();
    setActionPending(true);
    setActionError(null);
    try {
      const response = await api.revokeVerificationApproval(
        finding.id, approvalID, revocationRationale, request.signal);
      if (request.signal.aborted) return;
      setApprovals((previous) => previous.data ? {
        data: { ...previous.data, items: replaceByID(previous.data.items, response.approval) },
        pending: false, error: null,
      } : previous);
      setRevocationRationale("");
    } catch (cause) {
      if (!request.signal.aborted) setActionError(actionError(cause, "Verification approval revocation was not confirmed."));
    } finally {
      requests.current.delete(request);
      if (!request.signal.aborted) setActionPending(false);
    }
  }

  async function queueVerification(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (actionPending || approvalID === "" || unresolved) return;
    const request = controller();
    const pending: PendingIntent = {
      approvalId: approvalID, idempotencyKey: crypto.randomUUID().replaceAll("-", ""),
      knownIds: new Set(history.data?.items.map((item) => item.id) ?? []),
    };
    setActionPending(true);
    setActionError(null);
    try {
      const response = await api.queueDeterministicVerification(finding.id, {
        approvalId: pending.approvalId, idempotencyKey: pending.idempotencyKey,
      }, request.signal);
      if (request.signal.aborted) return;
      setUnresolved(null);
      setHistory((previous) => {
        const items = replaceByID(previous.data?.items ?? [], response.verification);
        return {
          data: {
            apiVersion: response.apiVersion, dataOrigin: response.dataOrigin, items,
            total: Math.max(previous.data?.total ?? 0, items.length), nextCursor: previous.data?.nextCursor ?? null,
          },
          pending: false, error: null,
        };
      });
      setDetail({ id: response.verification.id, data: response.verification, pending: false, denied: false, error: null });
    } catch (cause) {
      if (request.signal.aborted) return;
      const error = actionError(cause, "Verification queue acknowledgement was not confirmed.");
      setActionError(error);
      if (error.code === "network" || error.code === "invalid-response" || error.httpStatus === null) {
        setUnresolved(pending);
      }
    } finally {
      requests.current.delete(request);
      if (!request.signal.aborted) setActionPending(false);
    }
  }

  function refreshHistory(cursor: string | null, append: boolean) {
    if (history.pending) return;
    const request = controller();
    setHistory((previous) => ({ ...previous, pending: true, error: null }));
    void api.deterministicVerifications(finding.id, 100, cursor, request.signal).then(
      (page) => {
        if (request.signal.aborted) return;
        const items = append
          ? [...new Map([...(history.data?.items ?? []), ...page.items].map((item) => [item.id, item])).values()]
          : page.items;
        setHistory({ data: { ...page, items }, pending: false, error: null });
        const pending = unresolvedRef.current;
        const match = pending && page.items.find((item) =>
          item.approvalId === pending.approvalId && !pending.knownIds.has(item.id));
        if (match) {
          setUnresolved(null);
          setActionError(null);
          setDetail({ id: match.id, data: match, pending: false, denied: false, error: null });
        }
      },
      (cause: unknown) => {
        if (!request.signal.aborted) {
          const error = actionError(cause, "Unable to refresh verification history.");
          setHistory((previous) => ({
            data: ["forbidden", "not-found"].includes(error.code) ? null : previous.data,
            pending: false, error,
          }));
        }
      },
    ).finally(() => requests.current.delete(request));
  }

  function selectVerification(item: DeterministicVerification) {
    setDetail({ id: item.id, data: item, pending: false, denied: false, error: null });
  }

  function refreshDetail() {
    if (!detail.id || detail.pending) return;
    const request = controller();
    const id = detail.id;
    setDetail((previous) => ({ ...previous, pending: true, error: null }));
    void api.deterministicVerification(finding.id, id, request.signal).then(
      (response) => {
        if (!request.signal.aborted) {
          setDetail({ id, data: response.verification, pending: false, denied: false, error: null });
        }
      },
      (cause: unknown) => {
        if (request.signal.aborted) return;
        const error = actionError(cause, "Unable to refresh the selected verification.");
        const denied = ["forbidden", "not-found"].includes(error.code);
        setDetail((previous) => ({
          id, data: denied || previous.denied ? null : previous.data,
          pending: false, denied: denied || previous.denied, error,
        }));
      },
    ).finally(() => requests.current.delete(request));
  }

  if (!open) {
    return <div className="verification-entry"><Button type="button" variant="outline"
      onClick={openPane}>Show deterministic verification</Button></div>;
  }

  return <section className="finding-verification" aria-label="Deterministic verification">
    <header className="verification-heading"><h3>Deterministic verification</h3></header>
    <p className="verification-warning">
      Synthetic fixture reproduction is not proof of a real vulnerability and does not close or classify the finding.
    </p>
    <FormError error={unresolved ? null : actionErrorState} />
    {unresolved && <p role="alert" className="verification-warning">
      The queue acknowledgement was not confirmed. This verification intent remains unresolved until authorized history reconciles it.
    </p>}
    <div className="verification-errors"><FormError error={evidence.error} />
      <FormError error={approvals.error} /><FormError error={history.error} /></div>
    <div className="verification-records">
      {evidence.data?.items.map((item) => <p key={item.id}><code>{item.id}</code> {item.environmentId}</p>)}
      {approvals.data?.items.map((item) => <p key={item.id}><code>{item.id}</code>{" "}
        {item.revokedAt ? "Revoked approval" : item.current ? "Current approval" : "Approval not current"}</p>)}
    </div>

    {canWrite && <form aria-label="Submit synthetic fixture" className="verification-form"
      onSubmit={(event) => { void submitEvidence(event); }}>
      <h4>Submit synthetic fixture</h4>
      <label htmlFor={environmentField}>Environment</label>
      <input id={environmentField} value={environment} required onChange={(event) => setEnvironment(event.target.value)} />
      <label htmlFor={scopeField}>Scope revision</label>
      <input id={scopeField} value={scopeRevision} required onChange={(event) => setScopeRevision(event.target.value)} />
      <label htmlFor={conditionField}>Fixture condition</label>
      <select id={conditionField} value={condition} onChange={(event) => setCondition(event.target.value)}>
        <option value="true">true</option><option value="false">false</option>
      </select>
      <ActionButton type="submit" disabled={actionPending}>Submit fixture</ActionButton>
    </form>}

    {canApprove && <form aria-label="Approve synthetic fixture" className="verification-form"
      onSubmit={(event) => { void approveEvidence(event); }}>
      <h4>Approve synthetic fixture</h4>
      <label htmlFor={evidenceField}>Verification evidence</label>
      <select id={evidenceField} value={evidenceID} required onChange={(event) => setEvidenceID(event.target.value)}>
        {evidence.data?.items.map((item: DeterministicVerificationEvidence) =>
          <option value={item.id} key={item.id}>{item.environmentId} / {item.id}</option>)}
      </select>
      <label htmlFor={expiryField}>Approval expires at</label>
      <input id={expiryField} value={approvalExpiry} required placeholder="2026-10-09T12:00:00Z"
        onChange={(event) => setApprovalExpiry(event.target.value)} />
      <label htmlFor={rationaleField}>Approval rationale</label>
      <textarea id={rationaleField} value={approvalRationale} required
        onChange={(event) => setApprovalRationale(event.target.value)} />
      <ActionButton type="submit" disabled={actionPending || evidenceID === ""}>Approve fixture</ActionButton>
    </form>}

    {canApprove && currentApprovals.length > 0 && <div className="verification-form">
      <label htmlFor={revocationField}>Revocation rationale</label>
      <textarea id={revocationField} value={revocationRationale}
        onChange={(event) => setRevocationRationale(event.target.value)} />
      <ActionButton variant="outline" disabled={actionPending || approvalID === "" || revocationRationale.trim() === ""}
        onClick={() => { void revokeApproval(); }}>Revoke approval</ActionButton>
    </div>}

    {canWrite && <form aria-label="Queue deterministic verification" className="verification-form"
      onSubmit={(event) => { void queueVerification(event); }}>
      <h4>Queue deterministic verification</h4>
      <label htmlFor={approvalField}>Current approval</label>
      <select id={approvalField} value={approvalID} required onChange={(event) => setApprovalID(event.target.value)}>
        {currentApprovals.map((item: DeterministicVerificationApproval) =>
          <option value={item.id} key={item.id}>{item.environmentId} / {item.id}</option>)}
      </select>
      <ActionButton type="submit" disabled={actionPending || unresolved !== null || approvalID === ""}>
        Queue verification
      </ActionButton>
    </form>}

    <section className="verification-history">
      <header><h4>Verification history</h4><ActionButton variant="outline" disabled={history.pending}
        onClick={() => refreshHistory(null, false)}>Refresh verification history</ActionButton></header>
      <div className="verification-scroll" tabIndex={0}>
        <table aria-label="Verification history">
          <thead><tr><th scope="col">Verification</th><th scope="col">State</th><th scope="col">Action</th></tr></thead>
          <tbody>{history.data?.items.map((item) => <tr key={item.id}>
            <td><code>{item.id}</code><span>{item.environmentId}</span>
              <time dateTime={item.createdAt}>{timestampLabel(item.createdAt)}</time></td>
            <td>{label(item.state)}</td>
            <td><Button type="button" variant="outline" size="sm"
              onClick={() => selectVerification(item)}>Open verification</Button></td>
          </tr>)}</tbody>
        </table>
      </div>
      {history.data && <ActionButton variant="outline" disabled={history.pending || history.data.nextCursor === null}
        onClick={() => refreshHistory(history.data!.nextCursor, true)}>Load more verification history</ActionButton>}
    </section>

    <section className="verification-detail" aria-label="Selected verification">
      <header><h4>Selected verification</h4>{detail.id && <ActionButton variant="outline"
        disabled={detail.pending} onClick={refreshDetail}>Refresh verification</ActionButton>}</header>
      <FormError error={detail.error} />
      {detail.data && <div>
        <code>{detail.data.id}</code>
        <p role={detail.data.failure ? "alert" : "status"}>{label(detail.data.state)}</p>
        {detail.data.failure && <p><code>{detail.data.failure.code}</code> {detail.data.failure.message}</p>}
        {detail.data.result && <dl className="verification-result">
          <div><dt>Outcome</dt><dd>{detail.data.result.outcome}</dd></div>
          <div><dt>Evidence digest</dt><dd>{detail.data.result.evidenceDigest}</dd></div>
          <div><dt>Environment</dt><dd>{detail.data.result.environmentId}</dd></div>
          <div><dt>Scope revision</dt><dd>{detail.data.result.scopeRevision}</dd></div>
        </dl>}
        {detail.data.result && <p className="section-note">
          This synthetic result does not close the finding, classify a false positive, or verify a real vulnerability.
        </p>}
      </div>}
      {detail.id && !detail.data && <p className="section-note">Current verification metadata is withheld until an authorized refresh succeeds.</p>}
    </section>
  </section>;
}
