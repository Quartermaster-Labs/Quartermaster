// The self-update state machine, shared by everything that shows it.
//
// It is a store rather than component state because an update is a PROCESS-wide
// event with two windows onto it: the sidebar's update button and the Settings →
// System section. Both can start one, and both have to show the same download
// running -- two components each polling and each holding their own "updating"
// flag would give a user who clicked in Settings a sidebar that looks idle, and
// a second click there would bounce off the server's already-in-progress guard
// with no explanation.
//
// The work itself lives on the server (POST /api/update returns 202 and the
// download continues on the server's own lifetime), so this only ever polls and
// reports. Reloading the page does not cancel an update -- see resumePolling.

import { get, writable } from "svelte/store";
import type { UpdateStatus } from "../lib/types";
import { askConfirm, notify } from "../lib/confirm";
import { versionInfo } from "./api";

/** Last status the server reported, or null before the first read. */
export const updateStatus = writable<UpdateStatus | null>(null);
/** True from the moment an apply is accepted until it ends, one way or another. */
export const updateBusy = writable(false);
/** True while an on-demand release check is in flight. */
export const updateChecking = writable(false);
/** Why the last check failed, cleared by the next one. Null when it succeeded. */
export const updateCheckError = writable<string | null>(null);

let poller: ReturnType<typeof setInterval> | null = null;

function stopPolling(): void {
  if (poller !== null) {
    clearInterval(poller);
    poller = null;
  }
}

// The sidebar renders off `versionInfo`, which is fetched once per SSE connect.
// A check that finds a new release therefore has to write back into it, or the
// button that offers the update would not appear until the next reconnect.
function mirrorToVersionInfo(st: UpdateStatus): void {
  versionInfo.update((v) => ({
    ...v,
    update_available: st.available,
    latest_version: st.latest,
    release_url: st.release_url,
    update_blocked: st.blocked,
    update_restart: st.restart,
    update_phase: st.phase,
  }));
}

/** Reads the current status without starting anything. Null on failure. */
export async function fetchUpdateStatus(): Promise<UpdateStatus | null> {
  try {
    const r = await fetch("/api/update/status");
    if (!r.ok) return null;
    const st: UpdateStatus = await r.json();
    updateStatus.set(st);
    return st;
  } catch {
    return null;
  }
}

/**
 * Polls GitHub now instead of waiting out the server's six-hour tick, and
 * reports a failure in `updateCheckError` rather than swallowing it: a check
 * button that silently does nothing on a machine with no network reads as a
 * broken button.
 */
export async function checkForUpdates(): Promise<void> {
  if (get(updateChecking)) return;
  updateChecking.set(true);
  updateCheckError.set(null);
  try {
    const r = await fetch("/api/update/check", { method: "POST" });
    if (!r.ok) {
      updateCheckError.set((await r.text()).trim() || `check failed (${r.status})`);
      return;
    }
    const st: UpdateStatus = await r.json();
    updateStatus.set(st);
    mirrorToVersionInfo(st);
  } catch (e) {
    updateCheckError.set(e instanceof Error ? e.message : String(e));
  } finally {
    updateChecking.set(false);
  }
}

async function pollStatus(): Promise<void> {
  let st: UpdateStatus;
  try {
    const r = await fetch("/api/update/status");
    if (!r.ok) return;
    st = await r.json();
  } catch {
    // The server going away mid-poll is the EXPECTED end of an auto-restart
    // update, not a failure — keep the spinner and let the reload land.
    return;
  }
  updateStatus.set(st);

  if (st.phase === "error") {
    stopPolling();
    updateBusy.set(false);
    await notify("Update failed", st.error || "The update did not complete.");
    return;
  }
  if (st.phase !== "ready") return;

  // Swapped. Who restarts depends on how this install is run.
  stopPolling();
  if (st.restart === "manual") {
    updateBusy.set(false);
    await notify(
      `Update to ${st.latest} installed`,
      "Restart the Quartermaster service to finish — the new version is already in place.",
    );
    return;
  }
  // Auto: the server is shutting down and relaunching itself. Give it a moment
  // to come back on the same port, then reload into the new build.
  setTimeout(() => window.location.reload(), 4000);
}

function startPolling(): void {
  if (poller !== null) return;
  void pollStatus();
  poller = setInterval(() => void pollStatus(), 1000);
}

/**
 * Confirms, then starts the swap. Resolves as soon as the server has accepted
 * it — the download runs there, and progress arrives through `updateStatus`.
 */
export async function applyUpdate(): Promise<void> {
  if (get(updateBusy)) return;
  const v = get(versionInfo);
  const st = get(updateStatus);
  const latest = st?.latest || v.latest_version || "the latest version";
  const auto = (st?.restart ?? v.update_restart ?? "auto") === "auto";
  const ok = await askConfirm({
    title: `Update to ${latest}?`,
    body: auto
      ? "Quartermaster will download the new version in the background, then restart itself. Any loaded model is unloaded."
      : "Quartermaster will install the new version in the background. It runs as a service here, so restart the service when you're ready to switch to it.",
    confirmLabel: "Update",
  });
  if (!ok) return;
  updateBusy.set(true);
  updateStatus.set(null);
  try {
    const r = await fetch("/api/update", { method: "POST" });
    if (!r.ok) {
      await notify("Update failed", await r.text());
      updateBusy.set(false);
      return;
    }
  } catch (e) {
    await notify("Update failed", String(e));
    updateBusy.set(false);
    return;
  }
  // 202 accepted — the work is running server-side now.
  startPolling();
}

/**
 * Picks a running apply back up after a page reload. The swap runs on the
 * server, so reloading mid-download does not cancel it — it just leaves this
 * tab thinking nothing is happening, with a button that would bounce off the
 * server's in-progress guard.
 */
export function resumePolling(phase: string | undefined): void {
  if (get(updateBusy) || poller !== null) return;
  if (phase !== "downloading" && phase !== "verifying" && phase !== "staging") return;
  updateBusy.set(true);
  startPolling();
}

/** Phase → what a button or status line says while it is running. */
export function updateProgressLabel(st: UpdateStatus | null, busy: boolean, idle = "Update"): string {
  if (!busy) return idle;
  const p = st?.phase ?? "";
  if (p === "downloading" && st && st.total > 0) {
    return `${Math.round((st.done / st.total) * 100)}%`;
  }
  switch (p) {
    case "downloading":
      return "Downloading…";
    case "verifying":
      return "Verifying…";
    case "staging":
      return "Installing…";
    case "ready":
      return "Restarting…";
    default:
      return "Updating…";
  }
}

/** Download completion 0-100, for the progress bar. 0 outside the download. */
export function updatePercent(st: UpdateStatus | null): number {
  if (!st || st.phase !== "downloading" || !st.total) return 0;
  return Math.min(100, Math.round((st.done / st.total) * 100));
}
