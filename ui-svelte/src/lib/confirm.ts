import { writable } from "svelte/store";

// Promise-based replacement for window.confirm / window.alert.
//
// Native dialogs are the one piece of chrome we cannot theme: they render in the
// OS style, ignore dark mode, and block the whole tab. Call sites keep the same
// shape they had (`if (!(await askConfirm(...))) return`), so swapping them in is
// a one-line change per site.
//
// A single <ConfirmHost /> mounted at the app root renders whatever is in the
// store and settles the pending promise.

export interface ConfirmRequest {
  title: string;
  /** Optional second line. Rendered as plain text, never HTML. */
  body?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Red confirm button, for anything that destroys or restarts something. */
  danger?: boolean;
  /** Acknowledgement only: one button, no cancel (the old window.alert). */
  acknowledge?: boolean;
}

interface PendingConfirm extends ConfirmRequest {
  resolve: (ok: boolean) => void;
}

export const pendingConfirm = writable<PendingConfirm | null>(null);

/** Ask the user to confirm. Resolves true on confirm, false on cancel/dismiss. */
export function askConfirm(req: ConfirmRequest): Promise<boolean> {
  return new Promise((resolve) => {
    // Only one dialog at a time; a second request cancels the first rather than
    // stacking modals on top of each other.
    pendingConfirm.update((prev) => {
      prev?.resolve(false);
      return { ...req, resolve };
    });
  });
}

/** Show a message with a single dismiss button (the old window.alert). */
export function notify(title: string, body?: string): Promise<boolean> {
  return askConfirm({ title, body, acknowledge: true, confirmLabel: "OK" });
}
