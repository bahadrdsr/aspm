import { useId, useRef } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { motion } from "motion/react";
import type { APIError } from "@/api/client";
import { useModal } from "@/lib/use-modal";
import type { ModalFocusTarget } from "@/lib/use-modal";
import { usePreferences } from "@/lib/preferences";
import { Button } from "./ui/button";
import { Icon } from "./icon";

export function FormDialog({ title, description, returnFocus, onClose, children }: {
  title: string; description: string; returnFocus: ModalFocusTarget; onClose: () => void; children: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);
  const titleId = useId(), descriptionId = useId();
  const { reducedMotion } = usePreferences();
  useModal(dialog, closeButton, returnFocus, "assets-heading");
  return createPortal(<dialog ref={dialog} className="form-dialog" aria-labelledby={titleId} aria-describedby={descriptionId}
    onCancel={(event) => { event.preventDefault(); onClose(); }}>
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
