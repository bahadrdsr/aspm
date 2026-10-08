import { useState } from "react";
import { api, APIError } from "@/api/client";
import { useScopedAction } from "@/lib/use-scoped-action";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";

export function FindingHandoff({ findingId }: { findingId: string }) {
  const action = useScopedAction();
  const [message, setMessage] = useState<string | null>(null);
  function copy() {
    setMessage(null);
    void action.run(async (signal) => {
      const text = await api.findingHandoff(findingId, signal);
      if (!navigator.clipboard?.writeText) {
        throw new APIError("Clipboard access is unavailable. The handoff was not copied.", "unavailable", false);
      }
      try {
        await navigator.clipboard.writeText(text);
      } catch {
        throw new APIError("Clipboard access was denied. The handoff was not copied.", "unavailable", false);
      }
      return text.length;
    }, (length) => setMessage(`Copied ${length.toLocaleString("en-US")} characters of permission-checked context.`));
  }
  return <section className="detail-section" aria-label="Developer handoff">
    <div className="section-heading"><h3>Developer handoff</h3><span className="subtle-pill">Plain text</span></div>
    <p>Copy server-derived finding state and bounded evidence references. Raw evidence, analyst notes, and unmapped scanner fields are excluded.</p>
    <ActionButton variant="outline" disabled={action.pending} onClick={copy}>
      <Icon name="file" size={15} />Copy developer handoff
    </ActionButton>
    {action.pending && <p role="status">Preparing permission-checked handoff.</p>}
    {message && <p role="status">{message}</p>}
    <FormError error={action.error} />
  </section>;
}
