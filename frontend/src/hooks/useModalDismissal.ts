import { useEffect } from "react";
import { useCloseOnBack } from "./useCloseOnBack";

// Shared modal behavior: close on Escape, and lock body scroll while active
// so whatever's behind the modal can't scroll. Previously hand-rolled
// separately in CheckoutModal and AddToWorkflowDialog (the second copy's own
// comment said "matching CheckoutModal") — a future fix (e.g. nested-dialog
// scroll-unlock ordering) now only has to be applied here once.
//
// `active` defaults to true for a dialog that's only ever mounted while open
// (e.g. AddToWorkflowDialog, rendered conditionally by its parent); pass it
// explicitly for a component that stays mounted and toggles visibility
// itself (e.g. CheckoutModal's `open` prop).
export function useModalDismissal(onClose: () => void, active = true) {
  // The Android Back gesture closes the dialog instead of leaving the page
  // under it. The function useCloseOnBack returns is not needed here: every
  // dialog using this hook closes through its own onClose, and the hook takes
  // its history entry off when the dialog goes away.
  useCloseOnBack(onClose, active);

  useEffect(() => {
    if (!active) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [active, onClose]);

  useEffect(() => {
    if (!active) return;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = "";
    };
  }, [active]);

  // Keep Tab inside the dialog, and give focus back when it closes.
  //
  // Every dialog here already declares aria-modal, which PROMISES assistive
  // technology that the rest of the page is inert -- and nothing enforced it.
  // Tab walked straight out into the page behind, which for a screen-reader
  // or keyboard-only user meant operating controls they had been told were
  // not there, with no way back but Escape.
  //
  // In the hook rather than in each dialog, for the reason this file already
  // gives: four dialogs use it, and a trap implemented four times is a trap
  // implemented wrong three times.
  useEffect(() => {
    if (!active) return;
    // Where focus was before the dialog opened -- the row menu item, the
    // toolbar button. Putting it back is what lets somebody carry on down the
    // list instead of being dropped at the top of the document.
    const previous = document.activeElement as HTMLElement | null;
    const dialog = document.querySelector<HTMLElement>("[aria-modal='true']");

    // Focus the dialog itself rather than its first control: a dialog whose
    // first control is destructive should not open with that control armed
    // under the keyboard.
    if (dialog) {
      dialog.setAttribute("tabindex", "-1");
      dialog.focus({ preventScroll: true });
    }

    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Tab" || !dialog) return;
      // Queried per keypress, not once: these dialogs swap their own contents
      // (Import replaces its textarea with a preview card), so a list taken
      // on open goes stale while the dialog is still open.
      const focusable = dialog.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const on = document.activeElement;

      if (e.shiftKey && (on === first || on === dialog)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && on === last) {
        e.preventDefault();
        first.focus();
      }
    };

    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      // Only when focus is still inside the dialog. One that closed BECAUSE
      // the app navigated has already put focus where it wants it, and
      // yanking it back to a button that no longer exists is worse than
      // leaving it be.
      if (previous?.isConnected && dialog?.contains(document.activeElement)) {
        previous.focus({ preventScroll: true });
      }
    };
  }, [active]);
}
