import { useId, useRef } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import { motion } from "motion/react";
import type { APIError } from "@/api/client";
import { useModal } from "@/lib/use-modal";
import type { ModalFocusTarget } from "@/lib/use-modal";
import { usePreferences } from "@/lib/preferences";
import { Button } from "./ui/button";
import { Icon } from "./icon";

export function FormDialog({ title, description, returnFocus, onClose, children, containFocus = false }: {
  title: string; description: string; returnFocus: ModalFocusTarget; onClose: () => void; children: ReactNode;
  containFocus?: boolean;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);
  const titleId = useId(), descriptionId = useId();
  const { reducedMotion } = usePreferences();
  useModal(dialog, closeButton, returnFocus, "assets-heading");
  function trapTab(event: KeyboardEvent<HTMLDialogElement>) {
    if (event.key !== "Tab") return;
    event.stopPropagation();
    const controls = [...event.currentTarget.querySelectorAll<HTMLElement>("button, input, select, textarea, a[href], [tabindex]")]
      .filter((element) => element.tabIndex >= 0 && !element.matches(":disabled") && !element.closest("[inert]") &&
        element.getClientRects().length > 0 && getComputedStyle(element).visibility !== "hidden");
    const first = controls[0], last = controls.at(-1);
    if (first && last && (event.shiftKey ? document.activeElement === first : document.activeElement === last)) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    }
  }
  return createPortal(<dialog ref={dialog} className="form-dialog" aria-labelledby={titleId} aria-describedby={descriptionId}
    onKeyDown={containFocus ? trapTab : undefined}
    onCancel={(event) => { event.preventDefault(); event.stopPropagation(); onClose(); }}>
    <motion.div initial={reducedMotion ? false : { opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.18 }}>
      <header className="form-dialog-heading"><div><p className="eyebrow">Selected workspace</p><h2 id={titleId}>{title}</h2></div>
        <Button ref={closeButton} variant="ghost" size="icon" aria-label={`Close ${title.toLowerCase()}`} onClick={onClose}><Icon name="close" /></Button>
      </header>
      <p id={descriptionId} className="form-dialog-description">{description}</p>
      <div className="form-dialog-content">{children}</div>
    </motion.div>
  </dialog>, document.body);
}

export function FormError({ error }: { error: APIError | null }) {
  if (!error) return null;
  return <div role="alert" className="form-error"><Icon name={error.code === "forbidden" ? "lock" : "warning"} />
    <div><p>{error.message}</p>{error.requestId && <p className="request-id">Request <code>{error.requestId}</code></p>}</div>
  </div>;
}
