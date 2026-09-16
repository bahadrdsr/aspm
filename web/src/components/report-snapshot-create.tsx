import { useLayoutEffect, useRef } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type { ReportSnapshotResponse } from "@/api/types";
import { formText, inputText } from "@/lib/application-input";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { Button } from "./ui/button";

export function ReportSnapshotCreate({ freshnessDays, returnFocus, onClose, onAccepted }: {
  freshnessDays: number; returnFocus: HTMLElement; onClose: () => void; onAccepted: (result: ReportSnapshotResponse) => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const formRef = useRef<HTMLFormElement>(null);
  useLayoutEffect(() => {
    const dialog = formRef.current?.closest("dialog");
    if (!dialog) return;
    function trapTab(event: KeyboardEvent) {
      if (event.key !== "Tab" || !dialog) return;
      const controls = [...dialog.querySelectorAll<HTMLElement>("button, input, select, textarea, a[href], [tabindex]")]
        .filter((element) => element.tabIndex >= 0 && !element.matches(":disabled") &&
          !element.closest("[inert]") && element.getClientRects().length > 0 && getComputedStyle(element).visibility !== "hidden");
      const first = controls[0], last = controls.at(-1);
      if (first && last && (event.shiftKey ? document.activeElement === first : document.activeElement === last)) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      }
    }
    dialog.addEventListener("keydown", trapTab);
    return () => dialog.removeEventListener("keydown", trapTab);
  }, []);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    if (!form.reportValidity()) return;
    const data = new FormData(form);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      const name = inputText(formText(data, "name"), "Snapshot name", 256);
      const days = Number(formText(data, "freshnessDays"));
      if (!Number.isInteger(days) || days < 1 || days > 365) {
        throw new APIError("Freshness days must be an integer from 1 to 365.", "invalid-input", false);
      }
      return api.createReportSnapshot({ name, freshnessDays: days }, signal);
    }, onAccepted);
  }
  return <FormDialog title="Create snapshot" description={`Queue a saved posture report for ${workspace.name}. The reporting worker generates it separately; acceptance is not completion.`}
    returnFocus={returnFocus} onClose={onClose}>
    <form ref={formRef} aria-label="Create snapshot" className="application-form" onSubmit={submit} aria-busy={action.pending}>
      <fieldset className="form-grid" disabled={action.pending}>
        <legend className="sr-only">Snapshot details</legend>
        <label className="full-width">Snapshot name<input name="name" required maxLength={256} autoComplete="off" aria-describedby="snapshot-name-help" /></label>
        <p id="snapshot-name-help" className="form-help full-width">A nonblank name, up to 256 UTF-8 bytes. Saved results cannot be edited.</p>
        <label>Freshness days<input name="freshnessDays" type="number" min={1} max={365} step={1} required defaultValue={freshnessDays} /></label>
        <p className="form-help">Starts with the applied live window. The snapshot records its own as-of time when the worker runs.</p>
      </fieldset>
      <FormError error={action.error} />
      {action.pending && <p role="status" className="form-help">Waiting for the service's acknowledgement. Closing this form cannot undo a request already accepted.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || workspace.role === "viewer"}>Create snapshot</ActionButton></footer>
    </form>
  </FormDialog>;
}
