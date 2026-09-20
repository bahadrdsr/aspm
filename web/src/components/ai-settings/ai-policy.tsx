import { useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { aiApi } from "@/api/ai";
import { aiPolicyModes } from "@/api/ai-types";
import { inputChoice } from "@/lib/application-input";
import { ActionButton } from "@/components/action-button";
import { AIReadState, AITime } from "./ai-ui";
import { useAIMutation } from "./use-ai-data";
import type { AIData } from "./use-ai-data";

export function AIPolicySettings({ data, canWrite, onDenied }: { data: AIData; canWrite: boolean; onDenied: () => void }) {
  const { policy, policyRead, policies } = data;
  const action = useAIMutation(policies);
  const [confirmed, setConfirmed] = useState(policy);
  const [mode, setMode] = useState<string>(policy?.mode ?? "");
  const [error, setError] = useState<APIError | null>(null);
  if (confirmed !== policy) { setConfirmed(policy); setMode(policy?.mode ?? ""); }
  function refresh() { setError(null); policyRead.reload(); }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    void action.run(async (signal) => {
      if (!canWrite || !policy) throw new APIError("A workspace administrator and current policy metadata are required.", "forbidden", false);
      return aiApi.updatePolicy(inputChoice(mode, aiPolicyModes, "AI policy mode"), signal);
    }, () => setError(null), (cause) => { setError(cause); if (cause.code === "forbidden") onDenied(); });
  }
  return <section className="surface ai-section" aria-label="AI policy">
    <header className="ai-section-heading"><h3>AI policy</h3>
      <ActionButton variant="outline" disabled={policyRead.status === "loading"} onClick={refresh}>Refresh policy</ActionButton></header>
    <div className="ai-content">
      <AIReadState subject="policy" error={error ?? policyRead.error} pending={policyRead.status === "loading"} retry={refresh} />
      {policy && <div role="status" aria-label="Current AI policy" className="ai-selection">
        <p>Confirmed mode: <strong>{policy.mode}</strong></p><p>Revision: {policy.revision}</p>
        {policy.updatedAt === null ? <p>No configured update time or author.</p> : <p>Updated <AITime value={policy.updatedAt} /> by {policy.updatedBy}</p>}
      </div>}
      <p className="form-help">Disabled is deny-only. Local-only permits only family local, even when a hosted family endpoint is loopback.
        {" "}Approved-hosted is not itself a grant or execution permission. Configuration does not start an assessment.</p>
      <form aria-label="AI policy settings" className="application-form ai-form" onSubmit={submit}>
        <fieldset className="form-grid" disabled={!canWrite || !policy || action.pending}>
          <legend className="sr-only">Unsaved policy choice</legend>
          <label className="full-width">AI policy mode<select value={mode} required onChange={(event) => setMode(event.target.value)}>
            <option value="" disabled>Read the current policy first</option>
            {aiPolicyModes.map((value) => <option key={value} value={value}>{value}</option>)}
          </select></label>
          <p className="form-help full-width">This is an unsaved choice until the service confirms Save policy. The confirmed status above is separate.</p>
        </fieldset>
        {action.pending && <p role="status" className="form-help">Saving policy...</p>}
        <div className="ai-actions"><ActionButton type="submit" disabled={!canWrite || !policy || action.pending}>Save policy</ActionButton></div>
      </form>
    </div>
  </section>;
}
