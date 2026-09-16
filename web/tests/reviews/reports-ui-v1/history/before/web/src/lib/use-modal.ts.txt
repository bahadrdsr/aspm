import { useLayoutEffect } from "react";
import type { RefObject } from "react";
import { requestAuthority } from "@/api/authorization";

export type ModalFocusTarget = HTMLElement | RefObject<HTMLElement | null> | null;

export function useModal(
  dialog: RefObject<HTMLDialogElement | null>,
  initialFocus: RefObject<HTMLElement | null>,
  returnFocus: ModalFocusTarget,
  fallbackId: string,
) {
  useLayoutEffect(() => {
    const element = dialog.current;
    const root = document.documentElement;
    const body = document.body;
    const revision = requestAuthority().revision;
    const previous = { rootOverflow: root.style.overflow, bodyOverflow: body.style.overflow, x: window.scrollX, y: window.scrollY };
    root.style.overflow = "hidden";
    body.style.overflow = "hidden";
    element?.showModal();
    initialFocus.current?.focus({ preventScroll: true });
    return () => {
      element?.close();
      root.style.overflow = previous.rootOverflow;
      body.style.overflow = previous.bodyOverflow;
      if (requestAuthority().revision === revision) {
        window.scrollTo({ left: previous.x, top: previous.y, behavior: "instant" });
        const preferred = returnFocus && "current" in returnFocus ? returnFocus.current : returnFocus;
        const target = preferred?.isConnected ? preferred : document.getElementById(fallbackId);
        target?.focus({ preventScroll: true });
      }
    };
  }, [dialog, initialFocus, returnFocus, fallbackId]);
}
