import { writable, get } from "svelte/store";
import { syncSessions } from "../lib/sessionSync";
import type { ScoreOrigin } from "./musicHistory";

// The music playground's score library: every ABC score SheetSage2 extracted
// for this user, plus any they uploaded. Server-backed per playground user
// (GET/PUT /api/musicscores), hydrated with the other stores before the shell
// mounts, changes pushed back debounced. Kept apart from the music threads, so
// deleting a thread never takes its scores with it.
//
// `kind` says how the score got here: "extracted" from a recording (then
// `origin` names it), "uploaded" as a file, or "saved" from the composer.
export type ScoreKind = "extracted" | "uploaded" | "saved";

export interface LibraryScore {
  id: string;
  title: string;
  abc: string;
  kind: ScoreKind;
  origin?: ScoreOrigin;
  createdAt: number;
  updatedAt: number;
}

export const scoreLibrary = writable<LibraryScore[]>([]);

let synced = false;

export async function loadScoreLibrary(): Promise<void> {
  synced = false;
  try {
    const r = await fetch("/api/musicscores");
    const arr = r.ok ? await r.json() : [];
    scoreLibrary.set(Array.isArray(arr) ? arr : []);
  } catch {
    scoreLibrary.set([]);
  }
  synced = true;
}

export function clearScoreLibrary(): void {
  synced = false;
  scoreLibrary.set([]);
}

let timer: ReturnType<typeof setTimeout> | null = null;
scoreLibrary.subscribe((list) => {
  if (!synced) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    fetch("/api/musicscores", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(list),
    }).catch(() => {});
  }, 800);
});

function newId(): string {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

// Line endings and trailing space are not part of a score, so a file loaded
// twice, or re-extracted from the same take, is the same entry.
function sameScore(a: string, b: string): boolean {
  const norm = (s: string) => s.replace(/\r\n?/g, "\n").replace(/[ \t]+$/gm, "").trim();
  return norm(a) === norm(b);
}

// The tune's own T: line, else the fallback.
export function abcTitle(abc: string, fallback: string): string {
  const m = /^T:\s*(.+)$/m.exec(abc);
  const t = m?.[1].trim();
  return (t && t.toLowerCase() !== "untitled" ? t : fallback).slice(0, 80);
}

// Adds a score, or returns the entry already holding the same text (bumped to
// the top, and given an origin if it had none). Returns the entry's id.
export function addScore(abc: string, kind: ScoreKind, title: string, origin?: ScoreOrigin): string {
  if (!abc.trim()) return "";
  const now = Date.now();
  const list = get(scoreLibrary);
  const dup = list.find((s) => sameScore(s.abc, abc));
  if (dup) {
    scoreLibrary.set(list.map((s) => (s === dup ? { ...s, origin: s.origin ?? origin, updatedAt: now } : s)));
    return dup.id;
  }
  const entry: LibraryScore = { id: newId(), title: title.slice(0, 80) || "Score", abc, kind, origin, createdAt: now, updatedAt: now };
  scoreLibrary.set([entry, ...list]);
  return entry.id;
}

export function updateScore(id: string, patch: Partial<Pick<LibraryScore, "title" | "abc">>): void {
  scoreLibrary.update((list) => list.map((s) => (s.id === id ? { ...s, ...patch, updatedAt: Date.now() } : s)));
}

export function removeScore(id: string): void {
  scoreLibrary.update((list) => list.filter((s) => s.id !== id));
}

syncSessions("musicscores", scoreLibrary, () => null);
