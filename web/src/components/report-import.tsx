import { useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import { apiVersion, importFormats } from "@/api/types";
import type { Asset, ImportFormat, ImportInput, ImportMapping, ImportReceipt } from "@/api/types";
import { formText, inputChoice, inputText, readReportFile, sourceTimestamp, validateUploadSize } from "@/lib/application-input";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import type { AssetPagination } from "@/lib/use-asset-pages";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { Button } from "./ui/button";

const genericMapping: ImportMapping = {
  sourceFindingId: "id", title: "title", sourceSeverity: "severity", sourceLocation: "path",
  sourceLine: "line", impact: "impact", remediation: "remediation", description: "description",
};

const formatProfiles: Record<ImportFormat, { name: string; guidance: string }> = {
  sarif: { name: "SARIF 2.1.0", guidance: "SARIF JSON with version 2.1.0 and source results. Selecting a report does not run or verify a scan." },
  trivy: { name: "Trivy JSON", guidance: "Trivy JSON with SchemaVersion 2 and Results containing Target and Vulnerabilities. Other Trivy output profiles are not implied." },
  zap: { name: "ZAP JSON", guidance: "ZAP JSON with @version 2.16.1, site alerts and instances. XML is not supported; report URLs are literal context and are never fetched." },
  gitleaks: { name: "Gitleaks JSON", guidance: "Gitleaks JSON array of v8-style records with Fingerprint and Description, plus file/line context. Commit Date is not source scan time." },
  "generic-json": { name: "Generic JSON", guidance: "A JSON array of records using the displayed literal field profile. Names are record keys, not JSONPath or scripts." },
  "generic-csv": { name: "Generic CSV", guidance: "CSV with a header row and quoted records, using the same literal field profile as Generic JSON. Quoted commas, doubled quotes and embedded line endings stay unchanged in the upload." },
  manual: {
    name: "Manual JSON",
    guidance: "Human-authored structured JSON object, not prose extraction. Requires nonblank sourceFindingId and title (at most 4096 UTF-8 bytes each), and a sourceLocation object, which may be empty. Optional fields: severity, description, impact and remediation; sourceLocation.uri (up to 8192 UTF-8 bytes) and sourceLocation.line (0 through 2147483647).",
  },
};

export function ReportImport({ assets, assetPagination, returnFocus, onClose, onAccepted }: {
  assets: Asset[]; returnFocus: HTMLElement | null; onClose: () => void; onAccepted: (receipt: ImportReceipt) => void;
  assetPagination?: AssetPagination;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const [format, setFormat] = useState<ImportFormat>("sarif");
  const pendingStatus = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => {
    if (action.pending) pendingStatus.current?.focus({ preventScroll: true });
  }, [action.pending]);
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      const assetId = formText(data, "assetId");
      if (!assets.some((asset) => asset.id === assetId && asset.workspaceId === workspace.id)) {
        throw new APIError("Choose an asset from the selected workspace.", "invalid-input", false);
      }
      const format = inputChoice(formText(data, "format"), importFormats, "report format");
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
        ...((format === "generic-json" || format === "generic-csv") && { mapping: genericMapping }),
      };
      validateUploadSize(body);
      return api.importReport(body, signal);
    }, onAccepted);
  };
  return <FormDialog title="Import report" description={`Upload an existing local report to ${workspace.name}. Parsing happens asynchronously on the service, not in your browser.`}
    containFocus returnFocus={returnFocus} onClose={onClose}>
    <form aria-label="Import report" className="application-form" onSubmit={submit} aria-busy={action.pending}>
      <fieldset className="form-grid" disabled={action.pending}>
        <legend className="sr-only">Report and scan provenance</legend>
        <div className="form-field"><label htmlFor="import-asset">Asset</label><select id="import-asset" name="assetId" required defaultValue=""><option value="" disabled>Choose an asset</option>
          {assets.map((asset) => <option key={asset.id} value={asset.id}>{asset.name}</option>)}
        </select>
          {assetPagination?.visible && <>
            <ActionButton variant="outline" aria-disabled={assetPagination.pending || !assetPagination.hasMore} onClick={assetPagination.loadMore}>Load more assets</ActionButton>
            <p className="form-help" role={assetPagination.pending ? "status" : undefined}>
              {assetPagination.pending ? "Loading more assets. Your selection and draft stay in place." :
                assetPagination.hasMore ? "Only loaded assets are selectable. Load another page without changing your selection." : "No continuation remains in the last returned page."}</p>
          </>}
          {assetPagination?.error && <>
            <FormError error={assetPagination.error} /><ActionButton variant="outline" onClick={assetPagination.retry}>Retry assets</ActionButton>
          </>}
        </div>
        <div className="form-field"><label htmlFor="import-format">Format</label><select id="import-format" name="format" value={format}
          aria-describedby="import-format-help" onChange={(event) => {
            const selected = importFormats.find((value) => value === event.currentTarget.value);
            if (selected) setFormat(selected);
          }}>{importFormats.map((value) => <option key={value} value={value}>{formatProfiles[value].name}</option>)}</select></div>
        <p id="import-format-help" className="form-help full-width">{formatProfiles[format].guidance}</p>
        <label className="full-width">Report file<input type="file" name="report" required accept=".json,.sarif,.csv,application/json,text/csv,text/plain" aria-describedby="report-file-help" /></label>
        <p id="report-file-help" className="form-help full-width">UTF-8 text only. The raw file and complete encoded request must each fit within 8 MiB; the server may set a lower limit. Original line endings and trailing newlines are preserved. Filename and MIME type do not change your selected format.</p>
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
      {(format === "generic-json" || format === "generic-csv") && <details className="mapping-help"><summary>{formatProfiles[format].name} field profile</summary>
        <p>These same literal names are JSON record keys or CSV column headers. No scripts or external scanning are run.</p><pre>{JSON.stringify(genericMapping, null, 2)}</pre></details>}
      <FormError error={action.error} />
      {action.pending && <p ref={pendingStatus} tabIndex={-1} role="status" className="form-help">Reading and submitting the report. Closing cancels this browser request, but an acknowledged or already sent import may still run.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || workspace.role === "viewer"}>{action.pending ? "Submitting report..." : "Import report"}</ActionButton>
      </footer>
    </form>
  </FormDialog>;
}
