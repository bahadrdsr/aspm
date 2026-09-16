import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import { apiVersion } from "@/api/types";
import type { Asset, ImportInput, ImportMapping, ImportReceipt } from "@/api/types";
import { formText, inputChoice, inputText, readReportFile, sourceTimestamp, validateUploadSize } from "@/lib/application-input";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { Button } from "./ui/button";

const genericMapping: ImportMapping = {
  sourceFindingId: "id", title: "title", sourceSeverity: "severity", sourceLocation: "path",
  sourceLine: "line", impact: "impact", remediation: "remediation", description: "description",
};

export function ReportImport({ assets, returnFocus, onClose, onAccepted }: {
  assets: Asset[]; returnFocus: HTMLElement | null; onClose: () => void; onAccepted: (receipt: ImportReceipt) => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      const assetId = formText(data, "assetId");
      if (!assets.some((asset) => asset.id === assetId && asset.workspaceId === workspace.id)) {
        throw new APIError("Choose an asset from the selected workspace.", "invalid-input", false);
      }
      const format = inputChoice(formText(data, "format"), ["sarif", "generic-json"] as const, "report format");
      const sourceId = inputText(formText(data, "sourceId"), "Source ID", 512);
      const scanId = inputText(formText(data, "scanId"), "Scan ID", 512);
      const scope = {
        id: inputText(formText(data, "scopeId"), "Scope ID", 512),
        revision: inputText(formText(data, "revision"), "Scope revision", 512),
        branch: inputText(formText(data, "branch"), "Branch", 512),
      };
      const sourceScanAt = sourceTimestamp(formText(data, "sourceScanAt"));
      const sourceStatus = inputChoice(formText(data, "sourceStatus"), ["succeeded", "failed"] as const, "source status");
      const scanKind = inputChoice(formText(data, "scanKind"), ["full", "delta"] as const, "scan kind");
      const completeness = inputChoice(formText(data, "completeness"), ["complete", "partial", "unknown"] as const, "completeness");
      const file = data.get("report");
      const report = await readReportFile(file instanceof File ? file : null, signal);
      const body: ImportInput = {
        apiVersion, assetId, format, report, sourceId, scanId, scope, sourceScanAt,
        collectedAt: new Date().toISOString(), sourceStatus, scanKind, completeness,
        ...(format === "generic-json" && { mapping: genericMapping }),
      };
      validateUploadSize(body);
      return api.importReport(body, signal);
    }, onAccepted);
  };
  return <FormDialog title="Import report" description={`Upload an existing local report to ${workspace.name}. Parsing happens asynchronously on the service, not in your browser.`}
    returnFocus={returnFocus} onClose={onClose}>
    <form aria-label="Import report" className="application-form" onSubmit={submit} aria-busy={action.pending}>
      <fieldset className="form-grid" disabled={action.pending}>
        <legend className="sr-only">Report and scan provenance</legend>
        <div className="form-field"><label htmlFor="import-asset">Asset</label><select id="import-asset" name="assetId" required defaultValue=""><option value="" disabled>Choose an asset</option>
          {assets.map((asset) => <option key={asset.id} value={asset.id}>{asset.name}</option>)}
        </select></div>
        <div className="form-field"><label htmlFor="import-format">Format</label><select id="import-format" name="format" defaultValue="sarif"><option value="sarif">SARIF 2.1.0</option><option value="generic-json">Generic JSON</option></select></div>
        <label className="full-width">Report file<input type="file" name="report" required accept=".json,.sarif,application/json" aria-describedby="report-file-help" /></label>
        <p id="report-file-help" className="form-help full-width">UTF-8 text only. The complete encoded request must fit within 8 MiB; the server may set a lower limit. Original line endings are preserved.</p>
        <label>Source ID<input name="sourceId" required maxLength={512} autoComplete="off" /></label>
        <label>Scan ID<input name="scanId" required maxLength={512} autoComplete="off" /></label>
        <label className="full-width">Scope ID<input name="scopeId" required maxLength={512} autoComplete="off" /></label>
        <label>Scope revision<input name="revision" required maxLength={512} autoComplete="off" /></label>
        <label>Branch<input name="branch" required maxLength={512} autoComplete="off" /></label>
        <label className="full-width">Source scan time<input name="sourceScanAt" maxLength={64} placeholder="YYYY-MM-DDTHH:mm:ssZ" autoComplete="off" aria-describedby="source-time-help" /></label>
        <p id="source-time-help" className="form-help full-width">RFC3339 with a timezone. Leave blank if unknown; collection and import times do not replace it.</p>
        <div className="form-field"><label htmlFor="import-source-status">Source status</label><select id="import-source-status" name="sourceStatus" required defaultValue=""><option value="" disabled>Choose reported outcome</option><option value="succeeded">Succeeded</option><option value="failed">Failed</option></select></div>
        <div className="form-field"><label htmlFor="import-scan-kind">Scan kind</label><select id="import-scan-kind" name="scanKind" required defaultValue=""><option value="" disabled>Choose scan coverage</option><option value="full">Full</option><option value="delta">Delta</option></select></div>
        <div className="form-field"><label htmlFor="import-completeness">Completeness</label><select id="import-completeness" name="completeness" defaultValue="unknown"><option value="unknown">Unknown</option><option value="complete">Complete</option><option value="partial">Partial</option></select></div>
        <p className="form-help full-width">Use the scanner's actual scope and outcome. Only a newer successful complete full scan can support source-absence inference; it does not verify a resolution.</p>
      </fieldset>
      <details className="mapping-help"><summary>Generic JSON field profile</summary><p>For a JSON array of records, these literal field names are used. No scripts or external scanning are run.</p><pre>{JSON.stringify(genericMapping, null, 2)}</pre></details>
      <FormError error={action.error} />
      {action.pending && <p role="status" className="form-help">Reading and submitting the report. Closing cancels this browser request, but an acknowledged or already sent import may still run.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || workspace.role === "viewer"}>{action.pending ? "Submitting report..." : "Import report"}</ActionButton>
      </footer>
    </form>
  </FormDialog>;
}
