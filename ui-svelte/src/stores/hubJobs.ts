// Hub download jobs, shared by the Browse page and the Downloads manager.
//
// The job list lives on the server (internal/hub.Manager) and is POLLED, not
// pushed on the SSE bus — same as the backends installer. It sits in a store
// rather than in Browse.svelte because a 40 GB pull outlives the page that
// started it: the Downloads view, the Browse strip, and the sidebar badge all
// have to see the same jobs without three independent pollers.
import { writable, derived, get } from "svelte/store";
import { getHubJobs, type HubJob } from "../lib/hubApi";

export const hubJobs = writable<HubJob[]>([]);

// Bytes/sec per job id, measured between polls. The server reports a byte
// counter, not a rate — computing it here keeps that arithmetic out of every
// component that wants to show an ETA.
export const hubRates = writable<Record<string, number>>({});

// A running job is moving bytes (or about to). "paused" is deliberately NOT
// running: it is what stops the poller, so a job parked overnight doesn't keep
// the dashboard requesting every 1.5s.
export function isRunningJob(j: HubJob): boolean {
  return j.phase !== "paused" && j.phase !== "done" && j.phase !== "error" && j.phase !== "canceled";
}

export function isPausedJob(j: HubJob): boolean {
  return j.phase === "paused";
}

// Unfinished = running or paused. This is what the rail badge counts and what
// the menu lists as current work: a paused 40 GB pull is still outstanding, and
// dropping it into the finished history would be the same as losing it.
export function isUnfinishedJob(j: HubJob): boolean {
  return isRunningJob(j) || isPausedJob(j);
}

export const hubActiveCount = derived(hubJobs, ($j) => $j.filter(isUnfinishedJob).length);

let timer: ReturnType<typeof setInterval> | undefined;
let prev = new Map<string, { t: number; bytes: number }>();

function sampleRates(jobs: HubJob[]): void {
  const now = Date.now();
  const next = new Map<string, { t: number; bytes: number }>();
  const rates: Record<string, number> = {};
  for (const j of jobs) {
    const before = prev.get(j.id);
    next.set(j.id, { t: now, bytes: j.downloaded });
    if (!isRunningJob(j)) continue;
    if (before && now > before.t) {
      const inst = ((j.downloaded - before.bytes) * 1000) / (now - before.t);
      // Smoothed: a raw between-polls delta swings wildly on a chunked transfer
      // and an ETA that jumps between 4 min and 40 min is worse than none.
      const last = get(hubRates)[j.id] ?? inst;
      rates[j.id] = Math.max(0, last * 0.7 + inst * 0.3);
    }
  }
  prev = next;
  hubRates.set(rates);
}

export async function refreshHubJobs(): Promise<void> {
  try {
    const jobs = await getHubJobs();
    sampleRates(jobs);
    hubJobs.set(jobs);
    if (jobs.some(isRunningJob)) startHubPolling();
  } catch {
    // A transient poll failure (or a non-admin session) is not worth surfacing
    // here; the caller's next refresh retries.
  }
}

// Poll only while something is downloading — an idle dashboard makes no
// requests. A 40 GB pull is slow enough that 1.5s is plenty.
export function startHubPolling(): void {
  if (timer) return;
  timer = setInterval(async () => {
    try {
      const jobs = await getHubJobs();
      sampleRates(jobs);
      hubJobs.set(jobs);
      if (!jobs.some(isRunningJob)) stopHubPolling();
    } catch {
      // Ignored on purpose; the next tick retries.
    }
  }, 1500);
}

export function stopHubPolling(): void {
  clearInterval(timer);
  timer = undefined;
}
