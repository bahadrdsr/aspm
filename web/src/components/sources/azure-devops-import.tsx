import { useLayoutEffect, useRef } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { sourcesApi } from "@/api/sources";
import { apiVersion } from "@/api/types";
import type { ImportReceipt } from "@/api/types";
import type { CollectedSARIFImportInput, SourceRecord } from "@/api/source-types";
import { formText, inputChoice, inputText } from "@/lib/application-input";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { FormDialog, FormError } from "@/components/form-dialog";
import { Button } from "@/components/ui/button";

export function AzureDevOpsImportDialog({ record, returnFocus, onClose, onAccepted }: {
  record: SourceRecord;
  returnFocus: HTMLElement;
  onClose: () => void;
  onAccepted: (receipt: ImportReceipt) => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const pending = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      const body: CollectedSARIFImportInput = {
        apiVersion,
        format: "sarif",
        scope: {
          id: inputText(formText(data, "scopeId"), "Scope ID", 512),
          revision: inputText(formText(data, "revision"), "Scope revision", 512),
          branch: inputText(formText(data, "branch"), "Branch", 512),
        },
        sourceStatus: inputChoice(formText(data, "sourceStatus"), ["succeeded", "failed"] as const, "source status"),
        scanKind: inputChoice(formText(data, "scanKind"), ["full", "delta"] as const, "scan kind"),
        completeness: inputChoice(formText(data, "completeness"), ["complete", "partial", "unknown"] as const, "completeness"),
      };
      return sourcesApi.importAzureDevOpsRecord(record, body, signal);
    }, onAccepted);
  }
  return <FormDialog title="Import collected SARIF" containFocus returnFocus={returnFocus} onClose={onClose}
    description="Explicitly submit these already collected, verified report bytes to the existing SARIF intake. This action does not rerun the build or scanner.">
    <form className="application-form source-form" aria-label="Import collected Azure DevOps SARIF" aria-busy={action.pending} onSubmit={submit}>
      <fieldset className="form-grid" disabled={action.pending}>
        <legend className="sr-only">Collected report scan meaning</legend>
        <div className="source-selection full-width"><p><strong>Report record</strong></p><p><code>{record.id}</code></p>
          <p className="form-help">Native run: {record.nativeRunId}. Evidence digest: <code>{record.evidence.sha256}</code>.</p></div>
        <label className="full-width">Scope ID<input name="scopeId" required maxLength={512} autoComplete="off" /></label>
        <label>Scope revision<input name="revision" required maxLength={512} autoComplete="off" /></label>
        <label>Branch<input name="branch" required maxLength={512} autoComplete="off" /></label>
        <label>Source status<select name="sourceStatus" required defaultValue="">
          <option value="" disabled>Choose reported outcome</option><option value="succeeded">Succeeded</option><option value="failed">Failed</option>
        </select></label>
        <label>Scan kind<select name="scanKind" required defaultValue="">
          <option value="" disabled>Choose coverage type</option><option value="full">Full</option><option value="delta">Delta</option>
        </select></label>
        <label>Completeness<select name="completeness" required defaultValue="unknown">
          <option value="unknown">Unknown</option><option value="complete">Complete</option><option value="partial">Partial</option>
        </select></label>
        <p className="form-help full-width">Use the report's actual scope and outcome. Build success does not imply scanner success, full coverage or finding resolution.</p>
      </fieldset>
      <FormError error={action.error} />
      {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Submitting verified collected bytes. An acknowledgement means queued intake, not completed parsing.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || workspace.role === "viewer"}>Import collected report</ActionButton></footer>
    </form>
  </FormDialog>;
}
